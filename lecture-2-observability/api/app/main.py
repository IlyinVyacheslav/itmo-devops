"""
Подопытный сервис `api` для лабы 2.

Эндпоинты:
  GET /health                       -> "ok"
  GET /fail                         -> 500, спан помечается как error
  GET /slow                         -> спит 1-3 с во вложенном спане "slow-op"
  GET /load?n=100&c=10&path=/health -> n запросов к самому себе (c параллельно)
  GET /metrics                      -> метрики Prometheus (RED)

Переменные окружения:
  PORT                         порт (по умолчанию 8080)
  OTEL_SERVICE_NAME            имя сервиса в трейсах (по умолчанию "api")
  OTEL_EXPORTER_OTLP_ENDPOINT  куда слать трейсы по OTLP/HTTP, напр. http://jaeger:4318
                               если не задан — трейсы создаются (trace_id в логах есть),
                               но никуда не экспортируются
  LOG_LEVEL                    уровень логов (по умолчанию INFO)
"""
import asyncio
import json
import logging
import os
import random
import sys
import time
from contextlib import asynccontextmanager
from datetime import datetime, timezone

import httpx
from fastapi import FastAPI, Query, Request
from fastapi.responses import JSONResponse, PlainTextResponse, Response
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from opentelemetry.instrumentation.httpx import HTTPXClientInstrumentor
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace import Status, StatusCode
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Histogram, generate_latest

PORT = int(os.getenv("PORT", "8080"))
SERVICE_NAME = os.getenv("OTEL_SERVICE_NAME", "api")


# ---------------------------------------------------------------- logging (JSON + trace_id)
class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        payload = {
            "ts": datetime.fromtimestamp(record.created, tz=timezone.utc).isoformat(),
            "level": record.levelname.lower(),
            "logger": record.name,
            "msg": record.getMessage(),
            "service": SERVICE_NAME,
        }
        ctx = trace.get_current_span().get_span_context()
        if ctx.is_valid:
            payload["trace_id"] = format(ctx.trace_id, "032x")
            payload["span_id"] = format(ctx.span_id, "016x")
        extra = getattr(record, "fields", None)
        if extra:
            payload.update(extra)
        if record.exc_info:
            payload["exc"] = self.formatException(record.exc_info)
        return json.dumps(payload, ensure_ascii=False)


def setup_logging() -> logging.Logger:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter())
    level = os.getenv("LOG_LEVEL", "INFO").upper()
    root = logging.getLogger()
    root.handlers = [handler]
    root.setLevel(level)
    # uvicorn настраивает свои логгеры до импорта приложения — переводим их тоже в JSON
    for name in ("uvicorn", "uvicorn.error"):
        lg = logging.getLogger(name)
        lg.handlers = [handler]
        lg.propagate = False
    # свой access-лог пишем в middleware (с route/status/duration), штатный глушим
    logging.getLogger("uvicorn.access").disabled = True
    return logging.getLogger("api")


log = setup_logging()


# ---------------------------------------------------------------- tracing (OpenTelemetry)
provider = TracerProvider(resource=Resource.create({"service.name": SERVICE_NAME}))
if os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT"):
    # Экспортёр сам читает OTEL_EXPORTER_OTLP_ENDPOINT и добавляет /v1/traces
    provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
trace.set_tracer_provider(provider)
tracer = trace.get_tracer("api")
HTTPXClientInstrumentor().instrument()  # исходящие запросы /load тоже станут спанами


# ---------------------------------------------------------------- metrics (RED)
REQUESTS = Counter(
    "http_requests_total", "Total HTTP requests", ["method", "route", "code"]
)
ERRORS = Counter(
    "http_request_errors_total", "Total HTTP 5xx responses", ["method", "route", "code"]
)
LATENCY = Histogram(
    "http_request_duration_seconds",
    "HTTP request duration in seconds",
    ["method", "route"],
    buckets=(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 1.5, 2, 2.5, 3, 5, 10),
)


# ---------------------------------------------------------------- app
@asynccontextmanager
async def lifespan(app: FastAPI):
    log.info("api started", extra={"fields": {"port": PORT}})
    yield
    provider.shutdown()  # дослать накопленные спаны перед выходом
    log.info("api stopped")


app = FastAPI(title="api", lifespan=lifespan)


@app.middleware("http")
async def red_middleware(request: Request, call_next):
    start = time.perf_counter()
    try:
        response = await call_next(request)
        code = response.status_code
    except Exception:
        log.exception("unhandled error")
        response = JSONResponse({"error": "internal"}, status_code=500)
        code = 500
    elapsed = time.perf_counter() - start

    # Шаблон маршрута, а не сырой URL — чтобы не раздувать кардинальность меток
    route_obj = request.scope.get("route")
    route = getattr(route_obj, "path", "unmatched")
    method = request.method

    REQUESTS.labels(method, route, str(code)).inc()
    LATENCY.labels(method, route).observe(elapsed)
    if code >= 500:
        ERRORS.labels(method, route, str(code)).inc()

    if route != "/metrics":
        level = logging.ERROR if code >= 500 else logging.INFO
        log.log(
            level,
            "request handled",
            extra={"fields": {
                "method": method,
                "route": route,
                "path": request.url.path,
                "status": code,
                "duration_ms": round(elapsed * 1000, 1),
            }},
        )
    return response


@app.get("/health", response_class=PlainTextResponse)
async def health():
    return "ok"


@app.get("/fail")
async def fail():
    reason = random.choice(
        ["db connection timeout", "upstream returned garbage", "nil pointer in business logic"]
    )
    span = trace.get_current_span()
    err = RuntimeError(reason)
    span.record_exception(err)
    span.set_status(Status(StatusCode.ERROR, reason))
    log.error("simulated failure", extra={"fields": {"reason": reason}})
    return JSONResponse({"error": reason}, status_code=500)


@app.get("/slow")
async def slow():
    with tracer.start_as_current_span("slow-op") as span:
        delay = random.uniform(1.0, 3.0)
        span.set_attribute("slow.delay_seconds", round(delay, 3))
        log.info("doing slow operation", extra={"fields": {"delay_s": round(delay, 3)}})
        await asyncio.sleep(delay)
    return {"slept_seconds": round(delay, 3)}


@app.get("/load")
async def load(
    n: int = Query(100, ge=1, le=2000, description="сколько запросов сделать"),
    c: int = Query(10, ge=1, le=100, description="параллельность"),
    path: str = Query("/health", pattern="^/(health|fail|slow)$"),
):
    sem = asyncio.Semaphore(c)
    codes: dict[str, int] = {}

    async with httpx.AsyncClient(base_url=f"http://127.0.0.1:{PORT}", timeout=10) as client:
        async def one():
            async with sem:
                try:
                    r = await client.get(path)
                    key = str(r.status_code)
                except httpx.HTTPError:
                    key = "error"
                codes[key] = codes.get(key, 0) + 1

        start = time.perf_counter()
        await asyncio.gather(*(one() for _ in range(n)))
        elapsed = time.perf_counter() - start

    log.info("load finished", extra={"fields": {"n": n, "path": path, "codes": codes}})
    return {"requests": n, "path": path, "seconds": round(elapsed, 2), "codes": codes}


@app.get("/metrics")
async def metrics():
    return Response(generate_latest(), media_type=CONTENT_TYPE_LATEST)


# Инструментируем ПОСЛЕ объявления middleware: OTel-middleware оказывается внешним,
# поэтому в red_middleware уже есть активный спан и trace_id попадает в access-лог.
FastAPIInstrumentor.instrument_app(app, excluded_urls="metrics")
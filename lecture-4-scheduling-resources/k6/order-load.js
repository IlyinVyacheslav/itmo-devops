// Нагрузка по критическому пути: POST /order → postgres.
// Запуск:  k6 run k6/order-load.js   (предварительно: kubectl port-forward svc/shop-api 8080:80)
// Через --vus/--duration можно менять параллельность и длительность.
import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  scenarios: {
    order_path: {
      executor: 'constant-vus',
      vus: __ENV.VUS ?? 10,
      duration: __ENV.DURATION ?? '3m',
    },
  },
  // чек = «успешный путь заказа» (SLA в Части 8 считаем по этому чеку)
  thresholds: {
    checks: ['rate>0.95'],
  },
};

export default function () {
  const payload = JSON.stringify({ item: 'coffee', quantity: 1 });
  const res = http.post('http://localhost:8080/order', payload, {
    headers: { 'Content-Type': 'application/json' },
  });
  check(res, { 'order 201': (r) => r.status === 201 });
  sleep(0.1);
}

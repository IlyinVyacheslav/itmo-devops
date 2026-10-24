FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/batch .

FROM alpine:3.20

RUN adduser -u 10001 -D -H appuser
USER 10001

COPY --from=build /out/batch /usr/local/bin/batch
ENTRYPOINT ["batch"]

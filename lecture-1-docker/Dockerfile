FROM golang:1.26 AS builder

WORKDIR /src

COPY go.mod go.sum ./

RUN go mod download

COPY ./src/main.go main.go

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /api .

FROM scratch

COPY --from=builder /api /api

EXPOSE 8080

ENTRYPOINT ["/api"]
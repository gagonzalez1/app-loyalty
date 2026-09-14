# syntax=docker/dockerfile:1
FROM golang:1.25.13-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/puntazo-api ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/puntazo-migrate ./cmd/migrate

FROM alpine:3.21 AS runtime

RUN apk add --no-cache ca-certificates wget \
    && addgroup -S app \
    && adduser -S -G app -h /nonexistent -s /sbin/nologin app

FROM runtime AS migrate

COPY --from=build /out/puntazo-migrate /usr/local/bin/puntazo-migrate
COPY migrations /migrations

ENV MIGRATIONS_DIR=/migrations
USER app
ENTRYPOINT ["/usr/local/bin/puntazo-migrate"]
CMD ["up"]

FROM runtime AS api

COPY --from=build /out/puntazo-api /usr/local/bin/puntazo-api
COPY --from=build /out/puntazo-migrate /usr/local/bin/puntazo-migrate
COPY migrations /migrations

ENV MIGRATIONS_DIR=/migrations
USER app
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=6 \
  CMD wget -qO- http://127.0.0.1:8080/v1/health/live >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/puntazo-api"]

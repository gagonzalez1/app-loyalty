# syntax=docker/dockerfile:1
FROM golang:1.26.11-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/puntazo-api ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/puntazo-migrate ./cmd/migrate \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/puntazo-deletion-journal ./cmd/deletion-journal

FROM alpine:3.21.8 AS runtime

RUN apk upgrade --no-cache \
    && apk add --no-cache ca-certificates \
    && addgroup -S app \
    && adduser -S -G app -h /nonexistent -s /sbin/nologin app

FROM runtime AS migrate

COPY --from=build /out/puntazo-migrate /usr/local/bin/puntazo-migrate
COPY migrations /migrations
RUN chmod 0755 /migrations && chmod 0644 /migrations/*.sql

ENV MIGRATIONS_DIR=/migrations
USER app
ENTRYPOINT ["/usr/local/bin/puntazo-migrate"]
CMD ["up"]

FROM runtime AS api

COPY --from=build /out/puntazo-api /usr/local/bin/puntazo-api
COPY --from=build /out/puntazo-deletion-journal /usr/local/bin/puntazo-deletion-journal
COPY --from=build /out/puntazo-migrate /usr/local/bin/puntazo-migrate
COPY migrations /migrations
RUN chmod 0755 /migrations && chmod 0644 /migrations/*.sql

ENV MIGRATIONS_DIR=/migrations
USER app
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=6 \
  CMD wget -qO- http://127.0.0.1:8080/v1/health/live >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/puntazo-api"]

# syntax=docker/dockerfile:1
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/turaco-worker ./backend/cmd/turaco-worker
# Operator CLI (first administrator, emergency account); run with docker exec.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/turaco-admin ./backend/cmd/turaco-admin
# Migration runner and its SQL files (run as a one-shot container; see deploy/compose/full.yaml).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/turaco-migrate ./backend/cmd/turaco-migrate

FROM alpine:3.24
RUN addgroup -S -g 10001 app && adduser -S -u 10001 -G app app
COPY --from=build /out/turaco-worker /usr/local/bin/turaco-worker
COPY --from=build /out/turaco-admin /usr/local/bin/turaco-admin
COPY --from=build /out/turaco-migrate /usr/local/bin/turaco-migrate
COPY backend/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
USER app
ENTRYPOINT ["/usr/local/bin/turaco-worker"]

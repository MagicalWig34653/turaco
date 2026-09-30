# syntax=docker/dockerfile:1
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/turaco-worker ./backend/cmd/turaco-worker

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/turaco-worker /usr/local/bin/turaco-worker
USER app
ENTRYPOINT ["/usr/local/bin/turaco-worker"]

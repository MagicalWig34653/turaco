# syntax=docker/dockerfile:1
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/turaco-api ./backend/cmd/turaco-api

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/turaco-api /usr/local/bin/turaco-api
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/turaco-api"]

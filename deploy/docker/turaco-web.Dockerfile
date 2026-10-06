# syntax=docker/dockerfile:1
FROM node:24.21.0-alpine AS build
WORKDIR /src
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM nginx:1.31-alpine
COPY deploy/docker/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist /usr/share/nginx/html
EXPOSE 8080

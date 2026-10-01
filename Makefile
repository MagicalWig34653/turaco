SHELL := /bin/bash

# Explicit Go package roots: `./...` would also enter frontend/node_modules,
# which can contain Go packages (and tests) shipped by npm dependencies.
GO_PACKAGES := ./backend/... ./agents/... ./tools/...

.PHONY: help doctor bootstrap dev infra-up infra-down migrate api worker frontend test test-go test-frontend lint fmt fmt-check typecheck archcheck doccheck docs lockfiles-check docs-check check build docker-build clean

help:
	@printf '%s\n' \
	  'Turaco development commands' \
	  '' \
	  '  make doctor       Check local toolchain' \
	  '  make bootstrap    Install dependencies and initialize local development' \
	  '  make infra-up     Start PostgreSQL and local S3 mock in Colima/Docker' \
	  '  make migrate      Apply database migrations' \
	  '  make api          Run turaco-api natively' \
	  '  make worker       Run turaco-worker natively' \
	  '  make frontend     Run Vite dev server natively' \
	  '  make dev          Print the recommended local dev commands' \
	  '  make check        Run the repository quality gate' \
	  '  make build        Build backend, agents, and frontend' \
	  '  make docker-build Build production container images'

doctor:
	@./scripts/doctor.sh

bootstrap:
	@./scripts/bootstrap.sh

infra-up:
	@./scripts/compose.sh -f deploy/compose/dev.yaml up -d

infra-down:
	@./scripts/compose.sh -f deploy/compose/dev.yaml down

migrate:
	@./scripts/with-env.sh go run ./backend/cmd/turaco-migrate

api:
	@./scripts/with-env.sh go run ./backend/cmd/turaco-api

worker:
	@./scripts/with-env.sh go run ./backend/cmd/turaco-worker

frontend:
	@cd frontend && npm run dev

dev:
	@printf '%s\n' \
	  'Run these in separate terminals:' \
	  '  make infra-up && make migrate' \
	  '  make api' \
	  '  make worker' \
	  '  make frontend' \
	  '' \
	  'Frontend: http://localhost:5173'

test: test-go test-frontend

test-go:
	@./scripts/with-env.sh go test $(GO_PACKAGES)

test-frontend:
	@cd frontend && npm test

lint:
	@go vet $(GO_PACKAGES)
	@cd frontend && npm run lint

fmt:
	@gofmt -w $$(find backend agents tools -name '*.go' -type f)
	@cd frontend && npm run format

fmt-check:
	@./scripts/check-gofmt.sh
	@cd frontend && npm run format:check

typecheck:
	@cd frontend && npm run typecheck

archcheck:
	@go run ./tools/archcheck

doccheck:
	@go run ./tools/doccheck

docs:
	@go run ./backend/cmd/turaco-docgen

lockfiles-check:
	@./scripts/check-lockfiles.sh

docs-check:
	@go run ./backend/cmd/turaco-docgen -check
	@$(MAKE) doccheck

check: lockfiles-check fmt-check lint typecheck test archcheck docs-check
	@echo 'All quality checks passed.'

build:
	@mkdir -p dist/bin
	@go build -trimpath -o dist/bin/turaco-api ./backend/cmd/turaco-api
	@go build -trimpath -o dist/bin/turaco-worker ./backend/cmd/turaco-worker
	@go build -trimpath -o dist/bin/turaco-migrate ./backend/cmd/turaco-migrate
	@go build -trimpath -o dist/bin/connector-agent ./agents/connector
	@go build -trimpath -o dist/bin/endpoint-agent ./agents/endpoint
	@cd frontend && npm run build

# Uses the same Dockerfiles as CI/release.
docker-build:
	@docker build -f deploy/docker/turaco-api.Dockerfile -t turaco/api:dev .
	@docker build -f deploy/docker/turaco-worker.Dockerfile -t turaco/worker:dev .
	@docker build -f deploy/docker/turaco-web.Dockerfile -t turaco/web:dev .

clean:
	@rm -rf dist frontend/dist

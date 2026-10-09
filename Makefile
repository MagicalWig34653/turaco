SHELL := /bin/bash

# Explicit Go package roots: `./...` would also enter frontend/node_modules,
# which can contain Go packages (and tests) shipped by npm dependencies.
GO_PACKAGES := ./backend/... ./agents/... ./tools/...

.PHONY: help doctor bootstrap dev dev-setup infra-up infra-down lab-up lab-down lab-reset lab-verify migrate api worker frontend test test-go test-frontend lint fmt fmt-check typecheck archcheck load-test doccheck docs lockfiles-check docs-check check build docker-build clean site-build site-serve

help:
	@printf '%s\n' \
	  'Turaco development commands' \
	  '' \
	  '  make doctor       Check local toolchain' \
	  '  make bootstrap    Install dependencies and initialize local development' \
	  '  make infra-up     Start PostgreSQL and local S3 mock in Colima/Docker' \
	  '  make lab-up       Start optional lab services (Mailpit, Samba AD, Keycloak)' \
	  '  make lab-down     Stop lab services (keeps data)' \
	  '  make lab-reset    Stop lab services and delete their data' \
	  '  make migrate      Apply database migrations' \
	  '  make dev-setup    Start infra, migrate and create the dev admin' \
	  '  make api          Run turaco-api natively' \
	  '  make worker       Run turaco-worker natively' \
	  '  make frontend     Run Vite dev server natively' \
	  '  make dev          Print the recommended local dev commands' \
	  '  make check        Run the repository quality gate' \
	  '  make load-test    Run the development load generator (LOADTEST_ARGS="--profile ramp ...")' \
	  '  make build        Build backend, agents, and frontend' \
	  '  make site-build   Install dependencies and build the public website' \
	  '  make site-serve   Serve the built public website locally' \
	  '  make docker-build Build production container images'

doctor:
	@./scripts/doctor.sh

bootstrap:
	@./scripts/bootstrap.sh

infra-up:
	@./scripts/compose.sh -f deploy/compose/dev.yaml up -d

infra-down:
	@./scripts/compose.sh -f deploy/compose/dev.yaml down

lab-up:
	@./scripts/lab-up.sh

lab-down:
	@./scripts/compose.sh -f deploy/compose/lab.yaml down

lab-verify:
	@./scripts/lab-verify.sh

lab-reset:
	@./scripts/compose.sh -f deploy/compose/lab.yaml down -v

migrate:
	@./scripts/with-env.sh ./scripts/migrate.sh

dev-setup:
	@./scripts/dev-setup.sh

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
	@./scripts/with-env.sh go test -count=1 -p 1 $(GO_PACKAGES)

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

# Development-only load generator against a running local API (docs/development/load-testing.md).
# Not part of `make check`. Example: make load-test LOADTEST_ARGS="--profile ramp --rate-scale 0.25 --pg-url $$DATABASE_URL"
LOADTEST_ARGS ?= --profile smoke
load-test:
	@go run ./tools/loadtest run $(LOADTEST_ARGS)

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
	@go build -trimpath -o dist/bin/turaco-admin ./backend/cmd/turaco-admin
	@go build -trimpath -o dist/bin/connector-agent ./agents/connector
	@go build -trimpath -o dist/bin/endpoint-agent ./agents/endpoint
	@cd frontend && npm run build

site-build:
	@cd site && npm ci && npm run build

site-serve:
	@cd site && npm run serve

# Uses the same Dockerfiles as CI/release.
docker-build:
	@docker build -f deploy/docker/turaco-api.Dockerfile -t turaco/api:dev .
	@docker build -f deploy/docker/turaco-worker.Dockerfile -t turaco/worker:dev .
	@docker build -f deploy/docker/turaco-web.Dockerfile -t turaco/web:dev .

clean:
	@rm -rf dist frontend/dist

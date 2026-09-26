GO ?= go
COMPOSE ?= docker compose
COMPOSE_TEST := $(COMPOSE) -f docker-compose.yml -f docker-compose.test.yml
STATICCHECK := $(GO) run honnef.co/go/tools/cmd/staticcheck@latest
GOVULNCHECK := $(GO) run golang.org/x/vuln/cmd/govulncheck@latest

.DEFAULT_GOAL := help

.PHONY: help build test test-race cover lint fmt vet tidy up down logs ps migrate-up migrate-down test-integration e2e-up e2e e2e-fault e2e-down

help:
	@echo "build             compile every binary"
	@echo "test              unit tests (no Docker)"
	@echo "test-race         unit tests with the race detector (needs cgo)"
	@echo "cover             unit tests with per-package coverage"
	@echo "lint              gofmt, go vet, staticcheck and govulncheck"
	@echo "up / down / logs  single-instance stack with docker compose"
	@echo "test-integration  testcontainers suite: Postgres, Keycloak and LocalStack"
	@echo "e2e               three replicas plus the black-box suite over HTTP and SQS"
	@echo "e2e-fault         crash scenarios driven by FAULT_INJECT"

build:
	$(GO) build ./...

test:
	$(GO) test -count=1 ./...

test-race:
	$(GO) test -count=1 -race ./...

cover:
	$(GO) test -count=1 -cover ./...

fmt:
	@test -z "$$(gofmt -l ./cmd ./internal ./test)" || (gofmt -l ./cmd ./internal ./test && exit 1)

vet:
	$(GO) vet ./...
	$(GO) vet -tags integration ./test/integration/...
	$(GO) vet -tags e2e ./test/e2e/...

lint: fmt vet
	$(STATICCHECK) ./...
	$(GOVULNCHECK) ./...

tidy:
	$(GO) mod tidy

up:
	$(COMPOSE) up --build -d --wait

down:
	$(COMPOSE) down -v --remove-orphans

logs:
	$(COMPOSE) logs -f app

ps:
	$(COMPOSE) ps

migrate-up:
	$(COMPOSE) run --rm migrate up

migrate-down:
	$(COMPOSE) run --rm migrate down 1

test-integration:
	$(GO) test -count=1 -tags integration -timeout 20m ./test/integration/...

e2e-up:
	$(COMPOSE_TEST) up --build -d --wait

e2e: e2e-up
	$(GO) test -count=1 -tags e2e -timeout 15m ./test/e2e/...

e2e-fault:
	APP_ROLES=api,pending $(COMPOSE_TEST) up --build -d --wait
	E2E_FAULT=1 $(GO) test -count=1 -tags e2e -timeout 15m -run Fault ./test/e2e/...

e2e-down:
	$(COMPOSE_TEST) --profile fault down -v --remove-orphans

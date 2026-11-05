SHELL := /bin/bash

.PHONY: help deps-up deps-down migrate migrate-test sqlc tools test test-unit build vet fmt run-api run-worker

help:
	@echo "Monorepo targets (delegated to apps/api):"
	@echo "  make deps-up      Start postgres + redis dev stack"
	@echo "  make deps-down    Stop the dev stack"
	@echo "  make migrate      Apply DB migrations (DATABASE_URL)"
	@echo "  make migrate-test Apply migrations to the test DB"
	@echo "  make sqlc         Regenerate internal/db from queries/"
	@echo "  make test         Run all Go tests"
	@echo "  make test-unit    Run unit tests only (cmd + internal)"
	@echo "  make build        go build ./... in apps/api"
	@echo "  make vet          go vet ./... in apps/api"
	@echo "  make fmt          Check gofmt in apps/api"
	@echo "  make run-api      Run REST API (:8080)"
	@echo "  make run-worker   Run ingestion worker"

deps-up deps-down migrate migrate-test sqlc tools test test-unit build vet fmt run-api run-worker:
	$(MAKE) -C apps/api $@

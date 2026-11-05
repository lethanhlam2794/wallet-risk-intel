DATABASE_URL ?= postgres://wallet:devonly@127.0.0.1:5433/check_wallet?sslmode=disable
TEST_DATABASE_URL ?= postgres://wallet:devonly@127.0.0.1:5433/check_wallet_test?sslmode=disable
SQLC_VERSION ?= v1.30.0

.PHONY: deps-up deps-down migrate migrate-test sqlc tools test test-unit build vet fmt run-api run-worker

deps-up:
	docker compose up -d

deps-down:
	docker compose down

migrate:
	DATABASE_URL='$(DATABASE_URL)' go run ./cmd/migrate

migrate-test:
	DATABASE_URL='$(TEST_DATABASE_URL)' go run ./cmd/migrate

tools:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

sqlc:
	sqlc generate

test:
	go test ./...

test-unit:
	go test ./internal/... ./cmd/...

build:
	go build ./...

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker

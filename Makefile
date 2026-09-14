.PHONY: build test vet fmt fmt-check staticcheck lint check tidy up down up-all down-all

GOFILES := $(shell find . -name '*.go' -not -path './vendor/*')

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w $(GOFILES)

fmt-check:
	@test -z "$$(gofmt -l $(GOFILES))" || (echo "gofmt: the following files need formatting:"; gofmt -l $(GOFILES); exit 1)

staticcheck:
	staticcheck ./...

# Runs everything CI runs, in the same order, so a local pass predicts a CI pass.
check: fmt-check vet staticcheck test

tidy:
	go mod tidy

# up/down: just Postgres — what the test suite needs (§8: integration
# tests against real Postgres, not the app binaries). docker-compose.yml
# also defines collector/settler/indexer services now; up-all/down-all
# bring up the full stack (needs .env filled in — see .env.example).
up:
	docker compose up -d postgres

down:
	docker compose down

up-all:
	docker compose up -d --build

down-all:
	docker compose down

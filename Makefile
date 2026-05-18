# Server Assistant — dev tasks. Conforms to docs/CONVENTIONS.md.
# Single static binary, CGO disabled (ADR 0004 / ADR 0007).

BINARY := bin/server-assistant
PKG    := ./cmd/server-assistant

GOOSE_VERSION         := latest
SQLC_VERSION          := latest
GOLANGCI_LINT_VERSION := latest

.PHONY: build run test lint smoke sqlc tidy tools clean

build:
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) $(PKG)

run: build
	./$(BINARY) -config config.yaml

test:
	go test ./...

lint:
	golangci-lint run

# ADR 0008 boot contract: build the binary, boot it, wait for the ready log
# line, SIGTERM, assert clean exit. Pure (stdlib testing + os/exec, no new
# dependency); gated behind the `smoke` build tag so `make test` never runs it.
smoke:
	go test -tags smoke -count=1 -run TestSmokeBoot ./cmd/server-assistant

sqlc:
	sqlc generate

tidy:
	go mod tidy

# Install the dev tools this project depends on (not needed at runtime).
tools:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

clean:
	rm -rf bin

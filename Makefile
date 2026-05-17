# Server Assistant — dev tasks. Conforms to docs/CONVENTIONS.md.
# Single static binary, CGO disabled (ADR 0004 / ADR 0007).

BINARY := bin/server-assistant
PKG    := ./cmd/server-assistant

GOOSE_VERSION         := latest
SQLC_VERSION          := latest
GOLANGCI_LINT_VERSION := latest

.PHONY: build run test lint sqlc tidy tools clean

build:
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) $(PKG)

run: build
	./$(BINARY) -config config.yaml

test:
	go test ./...

lint:
	golangci-lint run

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

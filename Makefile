# Server Assistant — dev tasks. Conforms to docs/CONVENTIONS.md.
# Single static binary, CGO disabled (ADR 0004 / ADR 0007).

BINARY := bin/server-assistant
PKG    := ./cmd/server-assistant

# Pinned, never @latest: the prebaked offline image (ADR 0008) is only
# reproducible against fixed tool versions, and @latest is the silent drift
# CONVENTIONS exists to prevent. goose matches the go.mod require; sqlc and
# golangci-lint are the newest releases that still build on the Go 1.22 line.
# Bumping any of these is a deliberate change: rebuild the image in the same
# commit (ADR 0008 consequence; see tools/agent/README.md).
GOOSE_VERSION         := v3.21.1
SQLC_VERSION          := v1.27.0
GOLANGCI_LINT_VERSION := v1.59.1

.PHONY: build run test lint smoke sqlc tidy tools agent-result clean deploy demo-setup demo-e2e

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

# Deploy the built binary + demo config + hardened unit to the Mini Lab dev
# box (ADR 0023). SA_HOST overrides the default ssh alias (sa-dev).
deploy:
	bash scripts/deploy.sh

# (Re)provision the throwaway sa-demo-* container on Unraid used by the
# demo and demo-e2e (ADR 0023). SA_UNRAID/SA_DEMO_CONTAINER are overridable.
demo-setup:
	bash scripts/demo-setup.sh

# M2 harness end-to-end proof against the live Mini Lab deployment: kill
# sa-demo-web, watch the harness Diagnose/Approve/Actuate/recover (ADR
# 0009-0023). Not part of `make test` — needs the real deployment reachable.
demo-e2e:
	go test -tags demo -count=1 -timeout 15m -run TestDemoE2E ./test/demo

sqlc:
	sqlc generate

tidy:
	go mod tidy

# Install the dev tools this project depends on (not needed at runtime).
tools:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# The ADR 0008 sandbox gate: regenerate sqlc (db/ is gitignored), then run
# every gate, write RESULT.json beside the repo, and print the completion
# marker. RESULT.json + the marker stand in for sandcastle's maxIterations===1
# loop. Always writes RESULT.json, then exits non-zero if any gate failed.
agent-result:
	bash tools/agent/result.sh

clean:
	rm -rf bin RESULT.json

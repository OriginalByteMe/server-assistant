# Server Assistant

Monitoring + automation gateway for a single Unraid Host, running on a separate
box. See `CONTEXT.md` (glossary), `docs/adr/` (decisions), `docs/CONVENTIONS.md`
(engineering law), `docs/PLAN.md` (roadmap), `docs/issues/` (work items).

See **Current state** below for what's actually built and running today.

## Current state

v1 (the monitoring spine) is complete. The M2 LLM action harness — Diagnose
→ propose → Operator Approval → Actuate → judge recovery — is also built
and running live on the Mini Lab (ADR 0009–0022), with the dashboard as
this milestone's Approval surface (ADR 0023; Telegram remains the eventual
channel per ADR 0009). See `docs/DEMO.md` for the end-to-end demo runbook.

## Prerequisites

- Go 1.22+
- Dev tools (not needed at runtime): `make tools` installs `sqlc`, `goose`,
  `golangci-lint`.

## Build & run

```sh
make tidy        # resolve dependencies (first time)
make build       # CGO_ENABLED=0 single static binary -> bin/server-assistant
cp config.example.yaml config.yaml
make run
```

`make test` runs unit tests. `make lint` runs golangci-lint. `make sqlc`
regenerates the type-safe DB layer (consumed from issue 0002 onward).

## Layout

| Path | Role |
|---|---|
| `cmd/server-assistant` | composition root (`main`) |
| `internal/config` | typed config + `Source` (the ConfigSource seam) |
| `internal/core` | domain types + `Prober` / `Store` / `Notifier` seams |
| `internal/store` | SQLite (modernc) + embedded goose migrations + sqlc |
| `internal/prober`, `internal/notifier` | v1 stub implementations |

The four seams (CONVENTIONS rule 2) are defined here; richer implementations
attach behind them in later issues without reshaping the core (ADR 0006).

# Skeleton & seams

## What to build

Bootstrap the Go single-binary daemon and its four seams so every later slice
has rails. Module init building with `CGO_ENABLED=0` to a single static binary;
a composition root in `main`; a YAML config loader (typed structs, env-var
overrides for secrets only, versioned schema) where config is the source of
truth and SQLite never holds config; structured `slog` logging to stdout;
graceful shutdown on SIGTERM/SIGINT via `context` cancellation. Define the four
seam interfaces — `Prober`, `Store`, `Notifier`, `ConfigSource` — with only
stub/no-op implementations sufficient to boot. Wire `sqlc` + `goose` against an
empty schema and the lint toolchain (`gofmt`, `go vet`, `golangci-lint`).

Conforms to ADR 0004/0006/0007 and `docs/CONVENTIONS.md` (rules 1, 4, 6, 8).

## Acceptance criteria

- [ ] `CGO_ENABLED=0 go build` produces a single static binary
- [ ] Binary loads a YAML config, applies env overrides for secrets, rejects an invalid/unversioned config
- [ ] Structured `slog` output to stdout; no `fmt.Println` anywhere
- [ ] SIGTERM/SIGINT triggers graceful shutdown via context cancellation
- [ ] `Prober`, `Store`, `Notifier`, `ConfigSource` defined as interfaces; `main` wires stubs and exits cleanly
- [ ] `sqlc` + `goose` run against an empty schema; `golangci-lint` passes

## Blocked by

None - can start immediately

# Server Assistant — v1 Plan

v1 = the monitoring spine only. Scope, semantics, and rationale are fixed in
`CONTEXT.md` and `docs/adr/0001`–`0007`; libraries and design rules in
`docs/CONVENTIONS.md`. This plan must conform to all of them. M2 (LLM action
harness, two-way Approval, push agent, TSDB, UI-editable config, real auth) is
explicitly out and attaches behind the v1 seams — never reshaping them.

## Ordering principle

Seam-first, tracer-bullet (ADR 0006). Slices are sequential; each is atomic and
leaves a runnable binary. Build the lean implementation behind each seam; do
not pre-build M2.

## Slice 0 — Skeleton & seams

- Go module; `main` as composition root; `CGO_ENABLED=0` build.
- YAML config loader: typed structs, env-var overrides for secrets only,
  versioned schema. Config is the source of truth (rule 6).
- `slog` structured logging to stdout; graceful shutdown on SIGTERM/SIGINT via
  `context` cancellation (rule 4).
- Define seams as interfaces: `Prober`, `Store`, `Notifier`, `ConfigSource`.
- Wire `sqlc` + `goose` (empty schema), `golangci-lint`/`go vet`/`gofmt`.

**Done when:** binary boots, loads config, logs structured, exits cleanly.

## Slice 1 — Thinnest end-to-end vertical

- HTTP(S) Service probe with explicit timeout (rule 4).
- Status derivation UP/DEGRADED/DOWN (latency vs per-Service threshold);
  debounced commit (N consecutive agreeing Probes).
- SQLite `Store` via sqlc/goose: services, probe samples, committed status.
  SQLite holds runtime/history only, never config (rule 6).
- Server-rendered dashboard (HTMX, vendored, embedded): Service list with
  Status, latency, last-checked. SSE live updates on committed change.

**Done when:** point at one HTTP service, watch it flip UP/DEGRADED/DOWN live
on the page. Whole spine proven on one probe type.

## Slice 2 — Alerts

- Telegram `Notifier` behind the seam (`go-telegram/bot`).
- One-way Alert on committed Status change and on recovery to UP. Debounce
  (Slice 1) absorbs flapping.

**Done when:** phone buzzes once on down and once on recovery — no storm.

## Slice 3 — Host gate & UNKNOWN

- Host entity + Host-level reachability Probe.
- Gating: Host unreachable ⇒ its Services become UNKNOWN (not DOWN) and exactly
  one "Host unreachable" Alert fires. No code path collapses "can't tell" into
  "down" (rule 5, ADR 0005).

**Done when:** sever the path — one Alert + UNKNOWN Services, never a storm of
false DOWNs.

## Slice 4 — Full probe surface

- SSH into Unraid (`golang.org/x/crypto/ssh`): container-state probe; Host
  metrics probe (array/disk/parity, CPU/RAM).
- TCP/port probe for non-HTTP Services.
- Credential is a scoped, non-root, read-only Unraid user; secrets via
  env/file, never committed YAML, never logged (rule 7, ADR 0003 hygiene).

**Done when:** Core-4 + TCP probe set complete; the Host noun is fully realised.

## Slice 5 — History & deploy polish

- Rolling Probe-history retention window; dashboard trend sparkline.
- Per-Service config honored: latency threshold, debounce N, poll interval.
- Config file hot-reload (source of truth unchanged).
- systemd unit + deploy doc for the separate box.

**Done when:** v1 satisfies every ADR and CONVENTIONS rule; set-and-forget.

## Out of scope (M2, behind seams)

LLM action harness, two-way Approval, push agent on Unraid, dedicated TSDB,
UI-editable config, real authentication/security hardening. Each attaches
behind an existing v1 seam per ADR 0006; the security gate (ADR 0003) blocks
M2 until the Host-credential trust model is designed.

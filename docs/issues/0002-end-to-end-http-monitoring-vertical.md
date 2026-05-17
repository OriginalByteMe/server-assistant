# End-to-end HTTP monitoring vertical

## What to build

The thinnest complete path through every layer, proving the monitoring spine on
one probe type. An HTTP(S) Service probe with an explicit timeout produces
Probes; Status is derived (UP / DEGRADED when reachable but latency over the
per-Service threshold / DOWN) and commits only after N consecutive agreeing
Probes (debounce). Committed Status and probe samples persist to SQLite via
sqlc/goose (runtime and history only — never config). A server-rendered HTMX
dashboard (vendored JS, embedded assets, no build step) lists Services with
Status, latency, and last-checked, updating live via SSE on each committed
change.

Conforms to `CONTEXT.md` (Probe, Status, debounce) and ADR 0002/0004/0006.

## Acceptance criteria

- [ ] One HTTP Service defined in config is polled on an interval with an enforced timeout
- [ ] Status derives UP/DEGRADED/DOWN; DEGRADED = reachable but over latency threshold
- [ ] Status change commits only after N agreeing Probes; flapping does not commit
- [ ] Probe samples + committed Status persisted in SQLite; restart-safe
- [ ] Dashboard lists the Service with Status, latency, last-checked
- [ ] Open dashboard updates live via SSE on committed change with no refresh

## Blocked by

- 0001-skeleton-and-seams

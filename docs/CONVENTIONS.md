# Server Assistant — Engineering Conventions

The project's design law. Decisions here are deliberate and load-bearing.
Changing one is a real decision (often an ADR), not a casual refactor. The plan,
and every future change, must conform to this document.

## The tiebreaker (read this first)

When two approaches are reasonable, pick the one that needs the **least
maintenance in two years by one person at 2am**, not the one that is fastest to
write today. This generalises ADR 0004 and resolves most arguments before they
start.

## Chosen libraries

Every entry is a commitment. Adding a new third-party dependency requires
appending it here with: what it does, why stdlib won't, that it is actively
maintained, and that it is **pure Go**. No exceptions land silently.

| Concern | Choice | Why |
|---|---|---|
| Language / build | Go, `CGO_ENABLED=0`, single static binary, `embed.FS` for assets | ADR 0004; true static binary, trivial cross-compile, zero runtime |
| HTTP server | stdlib `net/http` (Go 1.22+ `ServeMux`) | No router framework until routing genuinely demands one |
| SQLite driver | `modernc.org/sqlite` (pure Go) | ADR 0007; preserves cgo-free static binary |
| SQL access | `sqlc` (codegen, type-safe) — **no ORM** | ADR 0007; explicit SQL, build-time safety, no runtime magic |
| Migrations | `pressly/goose` (pure Go, embedded SQL) | Simple, embeddable, no external tool at runtime |
| Config parsing | `goccy/go-yaml` | YAML is the homelab lingua franca; source-of-truth file |
| Scheduling | stdlib `time.Ticker` + worker pool, context-cancellable | No cron dependency for fixed-interval polling |
| SSH into Host | `golang.org/x/crypto/ssh` | The standard; stdlib-adjacent |
| HTTP/TCP probes | stdlib `net/http`, `net` with explicit timeouts | No dependency needed |
| Telegram | `go-telegram/bot` (actively maintained) | The one external integration dependency; isolated behind the Notifier seam |
| Dashboard JS | HTMX, vendored single file, no build | ADR 0004; SSE-driven, M2 approvals drop in |
| Logging | stdlib `log/slog`, structured, to stdout (journald via systemd) | No zap/zerolog; one event = one structured line |
| Testing | stdlib `testing` + `testify` (assert/require only) + `httptest` + in-memory SQLite | Adapters faked behind interfaces; no network in unit tests |
| Lint | `gofmt` + `go vet` + `golangci-lint` (staticcheck) | Standard, enforced in CI |

## Design rules

1. **Dependency minimalism.** Stdlib first. Every third-party dep is justified
   in the table above and must be pure Go. `CGO_ENABLED=0` is non-negotiable.
2. **Seams are sacred (ADR 0006).** `Prober`, `Store`, `Notifier`,
   `ConfigSource` are interfaces. v1 ships the lean implementation; richer
   backends (push agent, TSDB, UI-editable config, M2 action harness) attach
   *behind* these seams and must never reshape the core.
3. **Explicit over magic.** Hand-written SQL via sqlc. No ORM, no
   reflection-heavy frameworks, no global mutable state outside the composition
   root (`main`).
4. **Context everywhere.** Every external call (SSH, HTTP, TCP, Telegram, DB)
   takes a `context.Context` and an explicit timeout. The daemon shuts down
   gracefully on SIGTERM/SIGINT via context cancellation.
5. **The observer never lies (ADR 0005).** No code path may collapse "can't
   tell" into "down". UNKNOWN vs DOWN is enforced at the Status boundary.
6. **Config is the source of truth.** SQLite holds runtime state and history
   only — never configuration. The daemon is restart-safe and idempotent.
7. **Least privilege from day one (ADR 0003 hygiene).** The Host credential is
   a scoped, non-root, read-only Unraid user even though security hardening is
   deferred. Secrets come from env/file, never the committed YAML, never logs.
8. **Structured logging only.** `slog`, never `fmt.Println`. Never log secrets
   or credentials.
9. **Testable by construction.** Every adapter sits behind an interface with a
   fake. Integration tests use in-memory SQLite. Unit tests touch no network.
10. **Errors wrap, daemons don't panic.** `fmt.Errorf("...: %w", err)`; typed
    or sentinel errors at seams; no `panic` in any long-running daemon path.

## Definition of done

Code is not done because it compiles. An issue is done only when its acceptance
criteria are demonstrably met and the proof lives on the ticket — the exact
build/test/lint commands run, their outcome and commit SHA, and a ticked-off
acceptance checklist. Work that needs a human's eyes goes to **In Review**, not
**Done**; the agent never grades its own homework (ADR 0016). The mechanics —
what the completion comment must contain and the Done-vs-In-Review decision —
live in `docs/agents/issue-tracker.md`.

## How to change this document

A change to a library or rule is a deliberate decision. If it is hard to
reverse, surprising, or a real trade-off, write an ADR and link it here.
Otherwise, edit this file in the same commit as the change and say why in the
commit message. Silent drift is the failure mode this document exists to
prevent.

Rule 1 governs **runtime** dependencies only. The dev-time agent sandbox
(TypeScript/Docker under `tools/agent/`) is never compiled into or shipped with
the binary and does not count against it — see ADR 0008.

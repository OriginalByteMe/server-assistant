# Sandboxed agent development workflow

AI coding agents develop this repo **one issue at a time** inside **disposable,
offline, local isolated containers** (sandcastle, copy-in / branch-out): the
repo is copied into an ephemeral container, the agent must pass `make build` +
`make test` + `make lint` plus a stub smoke-boot (boot binary, wait for the
ready log line, SIGTERM, assert clean exit), and **only a git branch is copied
back out for human review** — the working tree is never mutated by an agent and
auto-merge never happens. The container image prebakes a **pinned Go toolchain
(`go`, `sqlc`, `goose`, `golangci-lint`) and the entire CONVENTIONS dependency
table into the module cache**, so every v1 slice — including Slice 4's
`x/crypto/ssh` and Slice 2's Telegram client — runs with zero network and zero
secrets, upholding CONVENTIONS rule 9. All meta-tooling (Dockerfile, sandcastle
harness, agent prompts) lives **in-repo under `tools/agent/`**, deliberately
accepting a TypeScript/Docker footprint in an otherwise pure-Go repo, because
one clone with a single place to bump the Go version beats two repos drifting
apart at 2am (the CONVENTIONS tiebreaker). This footprint is dev-time only; it
is never compiled into, nor shipped with, the single static binary, so
CONVENTIONS rule 1 (runtime dependency minimalism) is unaffected.

## Considered Options

The rejected alternatives are non-obvious and would otherwise be re-proposed:

- **Parallel agents across issues** — fights the explicitly sequential,
  dependency-ordered PLAN and adds merge/integration burden for a solo
  maintainer.
- **Bind-mount instead of copy-in** — a botched run reaches the host tree, so
  "break it freely" is not actually safe.
- **Remote Firecracker microVMs** — cost plus an external service dependency
  for a tiny solo homelab project, buying nothing once work is offline-only.
- **A separate sibling tooling repo** — keeps this repo pristine but creates
  cross-repo Go-version / tool drift, the exact 2am failure mode.
- **Network-allowed sandbox / `@latest` tools** — the silent drift CONVENTIONS
  exists to prevent; the LAW's dependency table already *is* the allowlist to
  prebake.
- **Auto-merge of green branches** — contradicts CONVENTIONS treating changes
  (especially to CONVENTIONS itself) as deliberate, ADR-worthy decisions.

## Consequences

- The first agent task must **pin the module graph** (`make tidy`, commit
  `go.mod` + `go.sum`) before any offline run is trusted; the prebaked cache is
  only reproducible against a pinned graph.
- A `make smoke` target (pure, no new dependency) is the contract for the
  boot/SIGTERM check.
- Sandcastle's structured-output mode requires `maxIterations === 1`; the loop
  instead uses a completion-signal marker plus a `RESULT.json` written by
  `make` and copied out beside the branch.
- Adding a CONVENTIONS-table dependency in a future slice requires a deliberate
  image rebuild to re-warm the cache, in the same change as the table edit.

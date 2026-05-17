# Go single static binary

## Context

Server Assistant is a solo-maintained daemon expected to run unattended for
years on a separate box, doing SSH + HTTP probing, SQLite persistence, Telegram
alerts, and a web dashboard, with an LLM action harness added in M2. The
operator's stated top priority is long-term sustainability / low maintenance.

## Decision

Built in Go as a single static binary with embedded SQLite and embedded static
dashboard assets. No language runtime, venv, or node_modules to keep alive on
the box over multi-year horizons; redeploy is "replace one file, restart
service".

## Consequences

- Dependency rot and runtime maintenance — the main multi-year risk for a
  set-and-forget daemon — are minimised.
- Slower initial feature velocity than Python/TS is accepted in exchange for
  near-zero long-term operational burden.
- Dashboard is server-rendered / embedded assets rather than a separately
  built-and-deployed SPA (keeps the "one binary" property).

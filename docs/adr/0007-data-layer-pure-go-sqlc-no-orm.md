# Data layer: pure-Go SQLite, sqlc, no ORM

## Context

ADR 0002 chose SQLite; ADR 0004 chose a Go single static binary whose entire
value is zero runtime maintenance over years. The data layer's driver and
access style either preserve or quietly destroy that property.

## Decision

- **Driver:** `modernc.org/sqlite` (pure Go), not `mattn/go-sqlite3` (cgo).
- **Access:** `sqlc` — hand-written SQL compiled to type-safe Go. No ORM.
- **Build:** `CGO_ENABLED=0` is mandatory and project-wide; any dependency
  requiring cgo is rejected by default.

## Considered Options

- **`mattn/go-sqlite3` (cgo) — rejected:** the fastest, most battle-tested
  driver, but cgo forces a C toolchain into every build, breaks easy
  cross-compilation, and yields a non-fully-static binary — directly negating
  ADR 0004. A homelab monitor will never hit the perf ceiling that would
  justify this.
- **ORM (GORM/ent) — rejected:** fast early CRUD, but reflection/opaque
  queries and migration churn are precisely the multi-year solo-maintenance
  costs ADR 0004 exists to avoid.
- **`database/sql` by hand — rejected:** zero deps, but hand-mapped scans are
  a recurring runtime-bug source; sqlc moves those failures to build time.

## Consequences

- The binary stays truly static and cross-compilable; no C toolchain ever.
- A `go:generate`/sqlc step exists in the dev workflow (not at runtime).
- modernc is younger and slower than the cgo driver; accepted as irrelevant at
  one-operator probe volume, and revisited only if a real, measured limit
  appears.
- `CGO_ENABLED=0` becomes a hard constraint on all future dependency choices
  (recorded in CONVENTIONS.md).

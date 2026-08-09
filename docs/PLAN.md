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


## M2 — Harness

M2 attaches the LLM action harness behind the v1 seams (ADR 0006), scoped
for a self-contained Mini Lab demo: kill `sa-demo-web` on Unraid, watch the
harness Diagnose (read-only), propose `restart_container`, wait for Operator
Approval on the dashboard, Actuate over the scoped write SSH credential, and
judge the recovery `recovered`. Slices actually built:

- **Store audit table** — `internal/store` migration 00004 + queries +
  `Store.SaveHarnessCycle`/`ListHarnessCycles`/`GetHarnessCycle`: the durable,
  untruncated per-cycle audit trail (ADR 0019).
- **Reasoner** — `internal/reasoner.Client`, an OpenAI-compatible chat+tool-call
  `core.Reasoner` over stdlib `net/http`, local-Ollama by default with cloud
  opt-in via config (ADR 0009, ADR 0013).
- **Read tools + Actuator** — `internal/tools` (`ContainerStatus`,
  `ContainerLogs`, `StatusHistory` as `core.ReadTool`s bound to the read-only
  SSH credential) and `internal/actuator.SSH`, a `core.Actuator` scoped to the
  closed restart-container catalog over the separate write SSH credential
  (ADR 0010, ADR 0011, ADR 0018, ADR 0021, ADR 0022).
- **Cycle engine** — `internal/harness.Harness`: read-only agentic Diagnosis,
  quarantined mutation behind Approval, single-flight, sticky fail-closed
  halt/re-arm, cooldown, and outcome judgement (ADR 0009, ADR 0014, ADR 0016,
  ADR 0017, ADR 0020). `Reconcile` runs once at startup and fail-closed
  resolves any cycle a prior process left non-terminal — pending-Approval
  becomes `expired`, dispatched-but-unobserved becomes `action_failed` — so
  a restart can never leave an incident stuck claiming an Operator can
  still act on it (ADR 0019).
- **Dashboard Approval surface** — `internal/web`'s `HarnessSource` seam plus
  the `/api/incidents*`, `/api/harness/*` JSON API and `/incidents` HTML
  routes: Alert presentation, Approve/Deny, and Halt/Re-arm on the existing
  dashboard, standing in for the not-yet-provisioned Telegram channel for
  this milestone only (ADR 0023). Approve/Deny persists the Operator's
  decision synchronously, before the cycle goroutine is signalled, so a
  crash between decision and dispatch can never silently lose it.
- **Config** — `internal/config.Harness`/`ReasonerConfig`/`Ceilings` wired
  into `Config` (rule 6: config is the only source of truth), plus
  `deploy/config.demo.yaml` for the Mini Lab deployment and a hardened
  `deploy/server-assistant.service` unit.

**Done when:** `make demo-e2e` passes against the live Mini Lab deployment
(`scripts/deploy.sh` to the sa-dev box, `scripts/demo-setup.sh` on Unraid) —
killing `sa-demo-web` drives a full commit-DOWN → Diagnose → propose →
Approve → Actuate → commit-UP → `recovered` cycle, conforming to ADR
0009–0023.

**Proven end to end:** `make demo-e2e` passes against the live Mini Lab
deployment with a local model (`sa-triage` on Ollama) — not mocked, not
shadow-mode. See `docs/DEMO.md` for the runbook.

## Pivot — Unraid-resident diagnostic and MCP control surface (issue #51)

The product direction changed while M2 was the live milestone: Server
Assistant is now built and shipped as an application that runs **on** the
Unraid host itself, aggregates full system state into a dashboard for the
human, and exposes that state plus a bounded set of mutating actions to *the
user's own LLM* over MCP — the product contains no inference of its own. This
section is an addition, not a rewrite: the v1/M2 history above is what was
actually built and stays as history; this section is what the product is now.

**Shipped this session, live and verified on `rijkaardserver`:**

- Unraid-resident dashboard (`http://100.90.134.29:8099/unraid`) and a
  stateless MCP endpoint (`http://100.90.134.29:8099/mcp`, 11 tools across 6
  categories: host, storage, containers, proposals, trends, scripts) —
  `docker compose ps` reports `server-assistant: Up (healthy)`.
- Key-free collectors: host vitals from `core.SourceProcfs` (`/host/proc`),
  array/share state from `core.SourceEmhttp` (`/var/local/emhttp` INI
  files), container state from the Docker socket, raw SMART via `smartctl`
  device passthrough — no `unraid-api` key needed for any of it.
- A SMART/capacity sampler (`internal/sampler`, migration `00005`) recording
  history only where history is the signal, 90-day retention, gaps recorded
  explicitly and never interpolated (CONVENTIONS rule 5).
- A script registry with a mandatory dry run: an LLM may draft a script, a
  human must review it, approval binds to a content hash (any edit returns
  it to pending), scripts take no arguments and may never write `/boot`, and
  a script that cannot complete a dry run cannot be approved. See ADR 0024
  for the amended execution boundary and `prototypes/dry-run/FINDINGS.md`
  for exactly what the dry-run sandbox can and cannot prove.

**Deliberately deferred, not done:**

- **Dashboard human authentication.** The dashboard and MCP endpoint run
  unauthenticated by Noah's explicit standing decision (2026-08-09); the
  script feature carries a visible unauthenticated warning until this lands.
  Machine auth for the MCP endpoint has a settled target (`unraid-api` API
  keys with roles and per-resource permissions) but that does not identify
  *who clicked Approve* — that is the deferred piece.
- **Tailscale Funnel.** Not enabled. `rijkaardserver` already carries the
  `funnel` node capability and the tailnet already has HTTPS certs issued
  (see `docs/research/mcp-reachability.md`), so turning it on later is a
  single approved command, not an ACL edit — but it has not been run, and
  running it exposes the dashboard/MCP surface to anyone who has or guesses
  the public URL, gated only by the approval/hash/dry-run model, never by
  authentication.
- **The always-on off-box observer.** Running on a separate physical box so
  it can observe the Host while degraded or down was the entire premise of
  ADR 0001 and the v1/M2 work above. The pivot inverts this: the product now
  runs *on* the Unraid host it monitors. The off-box observer becomes a
  later, separate effort — not abandoned, just no longer this codebase's job.
- **The full always-on monitoring spine** (probe/debounce/alert loop) is not
  part of this milestone; only the reduced SMART/capacity sampler above
  ships.

**What the previous Mini Lab deployment is now:** the v1/M2 work above still
runs and still passes — `make demo-e2e` against the live Mini Lab deployment
at tag `b186868` (issue #58) — but it is the *previous* shape of the product,
not the current one. It keeps running as a reference, and the SSH transport
it established is retained, unused, for the later off-box observer effort.

**Not a security boundary:** the docker socket this product's collectors and
executor use is root-equivalent — mounting it does not make the container
"isolated but capable" (the shipped `deploy/docker/docker-compose.yml` says
so plainly). Container packaging is a distribution convenience, not a
security boundary; what actually holds is the approval gate, hash-bound
argument-free scripts, and the mandatory dry run (ADR 0024).
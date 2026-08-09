# M2 Harness Demo — Runbook

## What this proves

The M2 LLM action harness — Diagnose (read-only) → propose an Action → wait
for a real Operator Approval → Actuate over a scoped write credential →
judge the recovery — works end to end against a live deployment, not just
in unit tests. `make demo-e2e` kills the throwaway `sa-demo-web` container
on the Unraid host, watches the v1 monitoring spine commit it DOWN, watches
a local Ollama model diagnose it and propose `restart_container`, approves
it through the dashboard API exactly as an Operator would, and asserts the
container is actually running again and the cycle is recorded `recovered`.
It is the proof that ADR 0009's default-deny Approval gate, ADR 0022's
credential split, and ADR 0017's single-flight cycle all hold together
under a real failure, not just against fakes.

## Topology

- **Deploy box (`sa-dev`):** Proxmox LXC 104 on the Mini Lab host
  `minilab`, 3 vCPU / 4 GB / 20 GB, Debian 13. Runs the `server-assistant`
  binary as a systemd service and a local Ollama instance.
- **Dashboard:** `http://100.96.198.75:8080` on the tailnet, or
  `http://192.168.68.61:8080` on the LAN. Both reach the same `sa-dev` LXC
  (ADR 0023 — no auth on this surface; LAN/Tailscale-only).
- **Local inference:** Ollama in the `sa-dev` LXC, model `sa-triage`
  (`deploy/Modelfile.sa-triage`, a `qwen2.5:1.5b-instruct` derivative with
  `num_thread 3` pinned — see Troubleshooting).
- **Unraid host (`rijkaardserver`, 192.168.68.57):** hosts the throwaway
  `sa-demo-web` busybox container the demo restarts, and the two scoped SSH
  forced-command credentials (ADR 0022) the harness uses to read and write
  it.

## One-time setup

```sh
bash scripts/unraid-credentials.sh   # generate + install the split read/write SSH keys (ADR 0022)
make deploy                          # build + ship the binary/config/unit/model to sa-dev (ADR 0023)
make demo-setup                      # (re)create the throwaway sa-demo-web container on Unraid
```

Run in this order: `unraid-credentials.sh` generates the keypairs on
`sa-dev` and authorizes them on Unraid; `make deploy` then ships the
binary, `deploy/config.demo.yaml`, the systemd unit, and builds the
`sa-triage` Ollama model, and depends on those keys already being in
place. `make demo-setup` just (re)creates the demo container and can be
re-run any time to reset it. All three are idempotent.

## Running it

```sh
make demo-e2e
```

Runs `go test -tags demo -count=1 -timeout 15m -run TestDemoE2E
./test/demo` against the live deployment (`SA_BASE_URL`, `SA_UNRAID`,
`SA_DEMO_CONTAINER`, `SA_DEMO_SERVICE` are overridable env vars; defaults
point at the addresses above and `sa-demo-web`). Wall-clock is roughly 70s
(a 15s settle margin before triggering, ~20s for the local model to
diagnose, then debounce + actuation + recovery polling). It asserts, in
order:

1. `/api/health` is reachable and reports `ok`.
2. Any leftover `pending` incident from a prior run is denied first — the
   harness serializes cycles globally (ADR 0017), so a stale pending
   Approval would otherwise hang the run.
3. The demo container is recreated and the spine observes it `running`.
4. `docker stop sa-demo-web` on Unraid triggers a commit-DOWN.
5. A new incident appears for `demo-web` with `trigger_status=DOWN`, a
   diagnosis proposing `restart_container`, a model name and positive
   latency recorded, at least one read tool call, and `approval=pending`.
6. `GET /incidents/{id}` renders the incident id and an `Approve` control.
7. `POST /api/incidents/{id}/approve` returns the incident with
   `approval=approved`.
8. The cycle reaches `outcome=recovered`, `resolved_target=sa-demo-web`,
   `dispatch_result=dispatched`.
9. The container is actually `running` again on Unraid.

## Driving it by hand

1. Stop the container: `ssh rijkaardserver docker stop sa-demo-web`.
2. Watch `http://192.168.68.61:8080/incidents` — a new row appears for
   `demo-web` once the spine commits DOWN, then fills in a proposed action
   once Diagnosis finishes (~20s on the local model). The list and detail
   pages are plain HTML (no SSE here yet); reload to see progress.
3. Open the incident's `detail` link and click **Approve**.
4. Reload the incident detail page — `Dispatch` shows the actuator result,
   and the outcome flips to `recovered` once the spine observes the
   container back UP.

## Safety rails

- The Action catalog is `restart_container` only, and
  `deploy/config.demo.yaml`'s `allow_restart` narrows it to `sa-demo-web`
  (ADR 0011).
- The write SSH credential's Host-side forced command
  (`deploy/unraid/write-only-command.sh`) independently enforces this: it
  only execs `docker restart sa-demo-*` or `docker version`, and refuses
  anything else with exit 77 — verified against `docker restart tdarr`.
  This is the outermost layer (ADR 0021): even a bug in the harness or a
  leaked write key cannot reach a production container.
- The read SSH credential (`deploy/unraid/read-only-command.sh`) is
  restricted the same way to a read-only allowlist (`docker inspect`,
  `docker logs`, `docker ps`, host load/mem) and cannot mutate anything at
  all.
- `scripts/demo-setup.sh` hard-refuses to touch any container whose name
  doesn't start with `sa-demo-`, even if `SA_DEMO_CONTAINER` is overridden.
- Resource caps: the demo container runs `--memory 64m --cpus 0.25`; the
  `sa-dev` LXC itself is capped at 3 vCPU / 4 GB / 20 GB.

## Troubleshooting

**Diagnosis takes minutes instead of seconds (ollama `num_thread`
cliff).** Ollama sizes its thread pool from the *host's* CPU count, not the
LXC's cgroup limit. On this box that's 4 vs. the LXC's 3 — the resulting
OpenMP oversubscription measured 0.5 tok/s (3m14s for one triage reply)
against 19 tok/s (6.8s) with `num_thread 3` pinned in
`deploy/Modelfile.sa-triage`. If the LXC's CPU count ever changes, rebuild
the model (`ollama create sa-triage -f deploy/Modelfile.sa-triage`) with a
matching pin, or diagnosis will silently crawl instead of failing loudly.

**Audit rows or probe samples go missing under load (`database is locked
(SQLITE_BUSY)`).** Multiple in-process writers (the probe loop, the
dashboard, a harness cycle's audit write) raced against SQLite's default
connection pool and lost writes silently. Fixed in `internal/store/sqlite.go`
via `SetMaxOpenConns(1)` (serializes writers through one connection) plus
`PRAGMA journal_mode = WAL` and `PRAGMA busy_timeout = 5000` (so an
external reader like `sqlite3 state.db` for support doesn't block the
daemon). If you see `SQLITE_BUSY` again, something is opening a second
connection to the same database file outside `Store.Open`.

**New incidents never appear / `make demo-e2e` hangs waiting for a
diagnosed incident.** The harness single-flights the whole cycle globally
(ADR 0017): only one Diagnosis→Approval→outcome cycle runs at a time, and
a fresh commit-DOWN while one is pending is dropped, not queued for
Approval. A leftover `pending` incident from a previous run (crashed test,
manual demo left mid-flight) will silently block every subsequent
incident. Check `GET /api/incidents?limit=50` for anything with
`approval=pending` and `POST /api/incidents/{id}/deny` it (or Deny it from
`/incidents/{id}`) before retrying.

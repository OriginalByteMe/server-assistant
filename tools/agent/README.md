# Agent-dev sandbox (`tools/agent/`)

The ADR 0008 sandbox: AI coding agents develop this repo **one issue at a
time** inside disposable, **offline**, local containers — **copy-in /
branch-out**. The repo is copied into an ephemeral container (never
bind-mounted), the agent runs fully offline, and **only a git branch is
copied back out for human review**. The host working tree is never mutated and
auto-merge never happens.

This is dev-time meta-tooling. Nothing here is compiled into or shipped with
the single static binary — CONVENTIONS rule 1 governs *runtime* deps only
(CONVENTIONS; ADR 0008).

| File | Role |
|---|---|
| `Dockerfile` | Frozen toolchain + the full module cache prebaked offline |
| `sandcastle.sh` | One-command default entry point: picks a runtime (Docker Desktop preferred), then hands off to `run.sh` (ARK-21) |
| `run.sh` | Copy-in / branch-out harness around a disposable container |
| `result.sh` | Gate runner: writes `RESULT.json` + the completion marker (via `make agent-result`) |
| `out/` | Per-run branch bundles + `RESULT.json` for review (gitignored) |

## One command: `/sandcastle <issue>` (the default path)

Sandboxed execution is the **default**, not a manual procedure (ARK-21; ADR
0008). The `sandcastle` skill (`.claude/skills/sandcastle/`) exposes one
command that picks a runtime and runs the issue in the sandbox:

```bash
# self-test / offline gate proof:
tools/agent/sandcastle.sh ARK-NN

# real issue work:
AGENT_CMD='<command that does the issue work, run at the repo root>' \
  tools/agent/sandcastle.sh ARK-NN

# first run, or after a CONVENTIONS dep-table / pinned-tool change:
tools/agent/sandcastle.sh --rebuild ARK-NN
```

`--rebuild`, `--keep`, `--branch NAME` and `AGENT_CMD` pass straight through
to `run.sh`, so everything below still applies — `sandcastle.sh` only adds
runtime selection in front of it:

- **Docker Desktop is preferred.** It is detected via its `desktop-linux`
  CLI context or a current Docker-Desktop daemon, and used first.
- **No silent host run.** If Docker Desktop is unavailable the command fails
  loudly and non-zero. The only alternative is an *explicitly* configured
  `SANDCASTLE_FALLBACK_DOCKER="<docker-like cmd>"` (e.g. `docker` for a plain
  Engine, or `podman`) — still a container runtime, never the host. There is
  deliberately no host-execution code path.

Prefer this over calling `run.sh` directly. `run.sh` remains the harness that
owns the ADR 0008 contract; `sandcastle.sh` is just the default front door.

## Run one issue end-to-end (the underlying harness)

```bash
# First run (or after a dependency/tool change): build the image. This is the
# ONE network step — it prebakes the toolchain and the entire module cache.
tools/agent/run.sh --rebuild ARK-NN

# Subsequent runs reuse the image and are fully offline:
AGENT_CMD='<command that does the issue work, run at the repo root>' \
  tools/agent/run.sh ARK-NN
```

What happens:

1. **Copy-in** — a clean `git clone --local --no-hardlinks` is `docker cp`'d
   into a fresh `--network=none` container. Never a bind-mount; a botched run
   cannot reach the host tree.
2. **Agent** — `AGENT_CMD` runs inside the container on branch
   `agent/ARK-NN`, fully offline, with zero secrets. Unset `AGENT_CMD` =
   bootstrap/self-test mode: skip the agent, just prove the gates pass
   offline.
3. **Gates** — `make agent-result` runs `sqlc → build → test → lint → smoke`,
   always writes `RESULT.json`, and prints the `SANDCASTLE_RESULT_COMPLETE`
   marker. Because sandcastle's structured-output mode forces
   `maxIterations === 1`, the harness confirms that one marker line and reads
   that one file instead of polling (ADR 0008).
4. **Branch-out** — only a `work.bundle` (the branch) and `RESULT.json` land
   in `tools/agent/out/ARK-NN-<stamp>/`. Nothing is merged.

Review and merge by hand:

```bash
cat   tools/agent/out/ARK-NN-<stamp>/RESULT.json          # verdict
git fetch tools/agent/out/ARK-NN-<stamp>/work.bundle agent/ARK-NN
git log -p FETCH_HEAD            # read every line; merge only if good
```

## Bootstrap caveat (ARK-20 itself)

The harness *is* the containment boundary, so it cannot run inside the
not-yet-existing sandbox. ARK-20 was developed on the host branch with the
gates and an offline `--network=none` container proof, then left for human
review — the same gate every later issue gets (ADR 0008).

## Deliberate image rebuild — required when the dependency table changes

The image prebakes a **fixed** module cache and **pinned** tools. It is
deliberately *not* auto-rebuilt: silent drift is exactly what CONVENTIONS and
ADR 0008 exist to prevent. Rebuild **in the same commit** as the change
whenever any of these move:

- a row is added/changed in the **`docs/CONVENTIONS.md` dependency table**
  (and therefore `go.mod`/`go.sum` after `make tidy`);
- a pinned tool version changes — `SQLC_VERSION`, `GOOSE_VERSION`,
  `GOLANGCI_LINT_VERSION` in the **Makefile** (mirror the same values in the
  `Dockerfile` ARGs — one bump point, two files);
- the Go toolchain line moves (the `Dockerfile` `FROM golang:1.22.x` and
  `go.mod`'s `go` directive must agree).

```bash
# After the table/tool/Go change is committed:
tools/agent/run.sh --rebuild ARK-NN     # re-warms the cache, then runs offline
```

If the cache is stale, the offline run fails closed (`GOPROXY=off`): a missing
module errors instead of silently fetching (CONVENTIONS rule 9). That failure
*is* the signal to rebuild — never relax the offline constraint to paper over
it.

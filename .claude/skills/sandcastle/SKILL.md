---
name: sandcastle
description: >
  Run an issue's agent task inside the ADR 0008 sandcastle — a disposable,
  offline, copy-in / branch-out container — instead of on the host. This is
  the DEFAULT execution path for every ready-for-agent slice. Use when asked
  to "run <issue> in the sandcastle / sandbox", "work ARK-NN sandboxed",
  invoke `/sandcastle ARK-NN`, or whenever about to execute agent work on a
  ready-for-agent issue in this repo.
---

# Sandcastle: sandboxed-by-default agent execution

Agent work on this repo runs **inside the sandcastle, not on the host** (ADR
0008). One command does it:

```bash
# self-test / offline gate proof (no agent):
tools/agent/sandcastle.sh ARK-NN

# real issue work — the agent command is the hook run.sh expects:
AGENT_CMD='<command that does the issue work, run at the repo root>' \
  tools/agent/sandcastle.sh ARK-NN

# first run, or after a CONVENTIONS dep-table / pinned-tool change:
tools/agent/sandcastle.sh --rebuild ARK-NN
```

`--keep` and `--branch NAME` pass straight through to `run.sh`.

## What you must do for `/sandcastle <issue>`

1. Confirm the issue id was given. No id → tell the user the usage; do **not**
   run anything (an empty run would self-test, not do their work).
2. Run `tools/agent/sandcastle.sh <issue>` from the repo root, with
   `AGENT_CMD` exported to the command that performs the issue's work when
   there is real work to do. Leave `AGENT_CMD` unset only for a bootstrap /
   offline gate proof.
3. Report the verdict: print the `RESULT.json` path and gate outcomes, and
   the `git fetch <bundle> agent/<issue>` line. **Never auto-merge** — only a
   human merges the review branch (ADR 0008).

## Non-negotiables (why this skill exists)

- **Docker Desktop is preferred.** `sandcastle.sh` detects it (its
  `desktop-linux` context or a current Docker-Desktop daemon) and uses it
  first.
- **No silent host run.** If Docker Desktop is unavailable the command fails
  loudly. The only alternative is an *explicitly* set
  `SANDCASTLE_FALLBACK_DOCKER` runtime — still a container, never the host.
  There is deliberately no host-execution code path.
- **The ADR 0008 contract is owned by `run.sh`**: copy-in (never bind-mount),
  `--network=none` offline run, branch-out of only a `work.bundle` +
  `RESULT.json`, host working tree never mutated, no auto-merge.
- This skill and `tools/agent/` are **dev-time meta-tooling** — never
  compiled into or shipped with the static binary (CONVENTIONS rule 1).

Full mechanics, the deliberate-rebuild rule, and review steps:
[`tools/agent/README.md`](../../../tools/agent/README.md).

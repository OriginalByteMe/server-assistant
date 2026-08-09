# Dry-run executor prototype — THROWAWAY, NOT PRODUCTION

Built to answer one question for [issue #54](https://github.com/OriginalByteMe/server-assistant/issues/54):
can a shimmed executor produce a useful and honest "what would happen"
transcript for a realistic Unraid maintenance script? Read
**[FINDINGS.md](FINDINGS.md)** for the answer, the exact rejection
predicate, and the honest ceiling. This directory is a prototype to react
to and is not wired into the Server Assistant service, has no tests beyond
the manual runs recorded in FINDINGS.md, and should not be imported from
production code.

## What's here

- `bin/` — PATH shims for `docker rm mv cp chmod chown mkfs dd rsync ln tee
  truncate`. Every shim logs its exact argv and returns a canned exit code;
  none of them ever perform the real operation.
- `safe-readonly.list` / `mutating.list` — the curated allowlist/mutation-list
  the rejection predicate classifies execve calls against.
- `run.sh` — orchestrates one dry run: real user+mount+net namespaces
  (`unshare`), the entire real filesystem bind-mounted back onto itself and
  remounted read-only, shims first on `PATH`, `strace -f -e trace=execve`
  for ground-truth exec tracing, a wall-clock `timeout`, then hands off to
  `predicate.sh` for the verdict.
- `predicate.sh` — evaluates the three-branch "cannot complete a dry run"
  rejection rule against one run's transcript + trace.
- `real-scripts/` — byte-exact copies of real scripts from the user's Unraid
  box (see `PROVENANCE.md`). Never executed on that host.
- `scenarios/*.env` — canned `docker` responses for a given dry run, used to
  demonstrate control-flow divergence (same script, different canned
  answers, different transcript).
- `transcripts/` — captured output from every run cited in FINDINGS.md.

## Running it

```
./run.sh <script> [scenario.env] [timeout-seconds]
DRYRUN_FIXTURE_MOUNTS="/mnt/user:/path/to/local/fixture" ./run.sh <script>
```

Requires `unshare` (util-linux, unprivileged user namespaces enabled) and
`strace` on the host; both are present on the dev machine this was built and
run on. No `sudo`, no privileged container, no network egress.

## Why this exists, why it's throwaway

This settles the *dry-run mechanism* question, not the product's design.
Real integration would need: a config-resolved scope of writable paths (this
prototype's scope is always empty — everything is a violation by design,
see FINDINGS.md), a docker-subcommand safety classification instead of
blanket interception, a UI for the transcript, and wiring into the approval
flow described in issue #51. None of that is here. Treat every design
decision in this directory as disposable.

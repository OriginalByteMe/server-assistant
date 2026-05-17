# LLM code execution is an explicit non-goal — read or write, sandboxed or not

Status: accepted (sharpens ADR 0009, which previously left read-only sandboxed
code-exec as "could qualify")

## Context

A standard agentic move is to give the LLM a sandboxed code-execution tool for
analysis (correlate timestamps, compute growth rates). ADR 0009 left this ajar
for the read-only case. Resolving it: a real sandbox (no network, no FS/proc
escape, resource caps) for arbitrary LLM-authored code is a security-critical
subsystem with no credible pure-Go option — it needs a separate process,
namespace, wasm, or VM boundary, colliding with CONVENTIONS rule 1 and the
2am-one-person tiebreaker. It also reintroduces "LLM authors code that runs on
the box," the exact category the harness architecture quarantines; "read-only"
does not save it, because a sandbox escape is the open shell again.

## Decision

Code execution of any kind is an **explicit non-goal** for the harness —
mutating or read-only, sandboxed or not. The LLM never authors code that runs
on the box. Richer Diagnosis is delivered only by adding typed read-only tools,
which are reviewed, ADR-grade code with statically known behavior — the same
discipline as the Action catalog.

## Consequences

- The harness's core promise stays absolute: blast radius is static and lives
  in reviewed code, never in LLM output — read *or* write. Guaranteed by "there
  is no code-exec," not by the weaker "the sandbox is correct."
- New diagnostic capability costs a typed-tool code change, not an LLM script;
  this friction is intentional.
- If a genuine need ever appears it requires its own hard-gated ADR; the
  default is no, not "maybe."

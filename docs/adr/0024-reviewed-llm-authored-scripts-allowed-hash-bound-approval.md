# Reviewed LLM-authored scripts are allowed; unreviewed execution remains forbidden

Status: accepted (amends ADR 0012 — narrows its blanket "LLM never authors
code that runs on the box" prohibition to the unreviewed case; settled in
issue #51's charting of the Unraid-resident pivot)

## Context

ADR 0012 made LLM code execution an explicit non-goal in any form —
authored, sandboxed, read or write — because a real sandbox has no credible
pure-Go option and "read-only" doesn't save it: a sandbox escape reopens the
same open shell. That reasoning still holds for *unreviewed* execution. The
product pivot settled in issue #51 changes what "the harness" is: there is
no bundled Reasoner driving Diagnosis inside this codebase anymore (see ADR
0025) — the LLM is the user's own, external, connected over MCP — and the
product's script feature is exactly the thing ADR 0012 would, read literally,
still ban: an LLM drafts a maintenance script, a human reviews it, approves
it, and it runs. Authorship was never the danger ADR 0012 named; running
something nobody reviewed was.

The `prototypes/dry-run/` prototype (see `FINDINGS.md`, answering issue #54)
proves the mandatory dry run this decision leans on is real evidence, not a
rubber stamp — it catches nonzero-exit failures and unshimmed mutation-risk
binaries a naive "did it exit 0" check would miss — while also proving its
limits: a script that branches on live `docker ps`/`docker inspect` state
receives the sandbox's canned answer, not the host's real one, so the exact
same byte-identical script can produce a clean transcript in one run and a
mutating one in the next purely because the canned answer differed; `docker
exec` is opaque to a host-level PATH shim entirely (whatever runs inside the
container is invisible); and a script written to run forever cannot be
dry-run to completion and is rejected outright rather than timing out into a
pass. None of that makes the dry run worthless — it makes it evidence, never
proof, which is why approval also requires the other four boundaries below.

## Decision

LLM **authorship** of scripts is allowed. LLM-authored **execution** of a
script requires all five of the following, enforced by the executor itself,
never by review discretion alone:

1. A human has reviewed the script and approved it.
2. Approval binds to a **content hash**, never a name or version label — any
   byte-level edit invalidates the approval and returns the script to
   pending.
3. The script accepts **no arguments** — the reviewed content is the entire
   behavior surface; an approved script cannot be re-parameterized by
   whoever reached the MCP endpoint.
4. The script may **never write `/boot`** (where `authorized_keys` and the
   forced-command wrappers live), enforced by the executor, not by asking a
   reviewer to catch it by eye.
5. A **dry run is mandatory** before approval can be granted — a script that
   cannot complete a dry run cannot be approved.

Code execution that skips any of the five remains exactly as forbidden as
ADR 0012 stated. Approvals ("Grants") are listable, revocable, and expiring.

**The Validation Sandbox inverts.** Issue #12 specified an *optional*
Validation Sandbox: post-live (validating an Action already in production
use), off-Host (sanitized replicas, not the real box), and typed-Action-only
(validating a closed catalog verb, never arbitrary content). This decision
makes it **mandatory** (no approval without a passing dry run), **pre-approval**
(gating execution before it ever runs, not building confidence after),
**on-Host** (the dry-run executor runs against the target script's real
dependency surface, not a sanitized replica), and **script-shaped** (it
validates an arbitrary reviewed script body, not a closed typed-Action
catalog). The only piece of the original design that survives unchanged is
the framing itself: **evidence, never authorization** — a dry run shows what
the sandbox was told the world looked like, not what the real host will do,
and the dashboard says "would do," never "will do."

## Consequences

- ADR 0012's core promise — blast radius lives in reviewed material, never in
  unconstrained LLM output — is preserved; what moved is *what counts as
  reviewed*: hash-bound human approval of one specific script body, not
  exclusively ADR-grade first-party code.
- A dry run's evidentiary limits are load-bearing product facts, not
  caveats to soften later: docker-state-dependent branches receive
  fabricated data, `docker exec` is opaque to the executor, and
  non-terminating scripts are rejected outright rather than approved by
  timeout. Any UI or doc describing a dry run says "evidence," never "proof."
- Container isolation is not what makes any of this safe — the docker socket
  the collectors and executor use is root-equivalent (see the compose file
  under `deploy/docker/`). What holds is the approval gate, the content-hash
  binding, the no-arguments rule, the `/boot` write ban, and the mandatory
  dry run — not the container boundary.
- If a genuine need ever arises for *unreviewed* LLM execution, ADR 0012's
  default-no posture still applies unchanged and still requires its own
  hard-gated ADR.

# Issue tracker: Linear

Issues and PRDs for this repo live in **Linear**, accessed via the **Linear MCP**.

- **Team:** Ark Personal projects
- **Project:** Smart Server assistant (inside the "Ark Personal projects" team)

## How to operate it

Use the **Linear MCP** for all issue operations (create, list, read, comment,
label, transition). The MCP's own tool descriptions are the authoritative
instructions for managing issues — follow those for exact call shapes.

Always scope work to the **"Smart Server assistant"** project within the
**"Ark Personal projects"** team. Do not create issues outside that project.

> **If the Linear MCP is not connected**, do not silently fall back to another
> tracker. Tell the user the Linear MCP needs to be connected before issue
> operations can run, and stop.

## When a skill says "publish to the issue tracker"

Create a Linear issue in the "Smart Server assistant" project (Ark Personal
projects team) via the Linear MCP.

## When a skill says "fetch the relevant ticket"

Read the issue from Linear via the MCP. The user will normally pass the Linear
issue identifier (or a URL) directly.

## Completion protocol (before Done)

An issue may only move to **Done** when its acceptance criteria are
demonstrably met and the evidence is on the ticket. Before transitioning any
issue to **Done** or **In Review**, post a single completion comment via the
Linear MCP containing:

1. **Acceptance-criteria checklist** — restate every `- [ ]` item from the
   issue body and mark it `- [x]`, each with a one-line note on how it was
   satisfied (the commit, file, or observed behaviour that meets it). If a box
   cannot honestly be ticked, the issue is **not** Done.
2. **Proof of tests** — the exact commands run and their outcome
   (e.g. `CGO_ENABLED=0 go build ./...`, `go test ./...`, `golangci-lint run`),
   pasted output or a faithful summary, and the commit SHA the proof was taken
   at. "Tests pass" without the command and result is not proof.
3. **Deviations / follow-ups** — anything done differently from the spec or an
   ADR, and any residual work spun out into a new issue (link it).

This completion comment is the **one sanctioned per-issue comment**. It is
net-new evidence, not a restatement of the body, so it is the explicit
exception to the "transition labels only, no brief comments" rule that governs
triage.

### Done vs In Review

- **Done** — the agent fully self-verified every acceptance criterion with
  automated proof (build, tests, lint) and nothing about the change needs human
  judgement. Post the completion comment, then transition to **Done**.
- **In Review** — use this state, *not* Done, whenever a human must look before
  the work is accepted. Post the same completion comment, transition to **In
  Review**, and assign / @-mention the reviewer. Choose In Review when any of:
  - an acceptance criterion is subjective or only human-verifiable (UX, visual,
    "feels right", on-host behaviour the agent cannot observe);
  - the change is hard to reverse or touches security, credentials, or
    destructive/mutating actions (ADR 0003 / 0014 / 0022);
  - the agent deviated from the spec or an ADR, or had to resolve an ambiguous
    criterion by interpretation;
  - the issue body explicitly asks for human sign-off.

When in doubt, prefer **In Review** over **Done**. An agent never marks its own
work Done when a human was asked to review it — this mirrors ADR 0016 ("the
actor never grades its own homework").

> **Workflow state vs triage label.** `In Review` is a *workflow state* for
> completed work awaiting human verification. It is distinct from the
> `ready-for-human` *triage label*, which marks an *un-started* issue that
> needs human implementation. Don't conflate them.

## Issue body conventions

Follow the existing convention used in `docs/issues/` (the legacy local issue
files — read a few for reference, e.g. `docs/issues/0001-skeleton-and-seams.md`,
`docs/issues/0011-read-only-diagnosis-vertical.md`). Each issue body uses:

- `# <Title>` — imperative, scoped
- `## What to build` — prose describing the slice; cite the governing
  decisions inline as `ADR NNNN` and the relevant `docs/CONVENTIONS.md` rule
  numbers
- `## Acceptance criteria` — a checkbox list (`- [ ]`), each item independently
  verifiable
- `## Blocked by` — referenced issues, or "None - can start immediately"

`docs/issues/` is the historical/local record and the source of truth for the
body format; new work is tracked in Linear using this same structure.

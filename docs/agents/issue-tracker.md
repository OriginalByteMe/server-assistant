# Issue trackers

Implementation issues and PRDs for this repo live in **Linear**, accessed via
the **Linear MCP**.

- **Team:** Ark Personal projects
- **Project:** Smart Server assistant (inside the "Ark Personal projects" team)

Wayfinder maps and their decision tickets are the sole exception: they live in
this repository's **GitHub Issues** so their native hierarchy and dependencies
are visible. See [Wayfinding operations](#wayfinding-operations-github-issues).

## How to operate Linear work

Use the **Linear MCP** for normal issue operations (create, list, read, comment,
label, transition). The MCP's own tool descriptions are the authoritative
instructions for managing issues — follow those for exact call shapes.

Always scope normal work to the **"Smart Server assistant"** project within the
**"Ark Personal projects"** team. Do not create issues outside that project.

> **If the Linear MCP is not connected**, do not silently move normal
> implementation work to another tracker. Tell the user the Linear MCP needs to
> be connected before those issue operations can run, and stop.

## When a skill says "publish to the issue tracker"

Create a Linear issue in the "Smart Server assistant" project (Ark Personal
projects team) via the Linear MCP, unless the invoking skill is Wayfinder.
Wayfinder uses the GitHub operations below.

## When a skill says "fetch the relevant ticket"

Read normal implementation work from Linear via the MCP. Read Wayfinder maps
and tickets from this repository's GitHub Issues. The user will normally pass
the issue identifier or URL directly.

## Wayfinding operations (GitHub Issues)

Wayfinder maps and their child decision tickets live in
`OriginalByteMe/server-assistant` GitHub Issues. Use GitHub's native issue
relationships; never emulate supported relationships in issue-body text.

### Labels and identity

- A map has `wayfinder:map`.
- Every child has exactly one of `wayfinder:research`,
  `wayfinder:prototype`, `wayfinder:grilling`, or `wayfinder:task`.
- The GitHub issue number is tracker identity, but human-facing references use
  a linked issue title rather than a bare number.

### Hierarchy, claims, and blocking

- Create every ticket first, then attach it to the map with GitHub's native
  sub-issue relationship (`POST
  /repos/{owner}/{repo}/issues/{map_number}/sub_issues`).
- Add blocking edges in a second pass with the native issue-dependency
  relationship (`POST
  /repos/{owner}/{repo}/issues/{ticket_number}/dependencies/blocked_by`).
- An open, unassigned child is unclaimed. Claim a ticket by assigning it to the
  developer driving the map before any work.
- A ticket is unblocked when every issue returned by its `blocked_by`
  dependency query is closed.
- The frontier is the map's open, unassigned, unblocked sub-issues, in sub-issue
  order.

If the GitHub API does not expose native sub-issues or dependencies, stop and
report the unavailable capability. Do not silently fall back to body sections.

### Charting and resolution

Charting creates the map, creates all currently sharp questions as unassigned
sub-issues, and wires dependencies. It does not claim or resolve a ticket.

To resolve one ticket:

1. Post the answer as a resolution comment.
2. Close the ticket as completed.
3. Append one linked-title gist to the map's `## Decisions so far` index.
4. Create newly sharp tickets before wiring their hierarchy/dependencies.
5. Remove graduated text from `## Not yet specified`.

If a ticket is beyond the destination, close it as not planned and append one
linked-title explanation under `## Out of scope`; do not add it to
`## Decisions so far`.

## Completion protocol for Linear implementation work

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

## Linear issue body conventions

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
body format; normal implementation work is tracked in Linear using this same
structure. Wayfinder ticket bodies follow the GitHub-specific convention above.

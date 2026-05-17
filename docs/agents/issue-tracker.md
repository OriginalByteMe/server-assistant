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

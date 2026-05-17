# Triage Labels

The triage skill speaks in terms of canonical triage roles, split into
**category** roles (what kind of issue) and **state** roles (where it is in the
workflow). Every triaged issue should carry exactly one of each. This file maps
those roles to the actual label strings used in this repo's issue tracker
(Linear).

## Category roles

| Canonical role | Label in our tracker | Meaning                    |
| --------------- | -------------------- | -------------------------- |
| `bug`           | `Bug`                | Something is broken        |
| `enhancement`   | `Feature`            | New feature or improvement |

Linear also ships a default `Improvement` label; we do **not** use it as the
category role — map the `enhancement` role to `Feature`. Roadmap-derived build
slices are `Feature`, not `Bug`.

## State roles

| Label in mattpocock/skills | Label in our tracker | Meaning                                  |
| -------------------------- | -------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`       | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`         | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`    | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`    | Requires human implementation            |
| `wontfix`                  | `wontfix`            | Will not be actioned                     |

`ready-for-human` is a *pre-work triage label* — it marks an un-started issue
that needs human implementation. Do not confuse it with the **In Review**
*workflow state*, which marks completed work awaiting human verification (see
the completion protocol in `docs/agents/issue-tracker.md`).

When a skill mentions a role (e.g. "apply the AFK-ready triage label"), use the
corresponding label string from these tables. Create the Linear label if it
does not yet exist in the "Smart Server assistant" project.

Edit the right-hand columns to match whatever vocabulary you actually use.

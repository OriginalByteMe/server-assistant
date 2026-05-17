# Host reachability gate & UNKNOWN status

## What to build

Introduce the Host entity and a Host-level reachability Probe. When the Server
Assistant box cannot reach the Host, that Host's Services become UNKNOWN (not
DOWN) and exactly one "Host unreachable" Alert fires — never one Alert per
Service. No code path may collapse "can't tell" into "down". When the Host
becomes reachable again, per-Service Status resumes without a double-alert
storm. The dashboard renders UNKNOWN distinctly from DOWN.

Conforms to ADR 0005 and `docs/CONVENTIONS.md` (rule 5).

## Acceptance criteria

- [ ] Host entity + Host-level reachability Probe exist
- [ ] Host unreachable ⇒ its Services show UNKNOWN, not DOWN
- [ ] Exactly one "Host unreachable" Alert fires, never one per Service
- [ ] Recovery restores per-Service Status and does not double-alert
- [ ] Dashboard renders UNKNOWN distinctly from DOWN

## Blocked by

- 0003-telegram-alerts

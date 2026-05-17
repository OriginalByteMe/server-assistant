# Observer blindness is UNKNOWN, not DOWN

## Context

Server Assistant pulls Probes from a separate box (ADR 0001). When that box's
own uplink or the path to Unraid fails, every pull Probe fails simultaneously
even though Unraid and its Services may be perfectly healthy. Treating that as
DOWN produces false-DOWN alert storms that train the operator to ignore Alerts.

## Decision

Status has a fourth value, **UNKNOWN**, meaning "the observer cannot determine"
— a statement about the observer's blindness, never a claim about the subject.
A Host-level reachability Probe gates its Services: if the Host is unreachable
from the Server Assistant box, its Services become UNKNOWN (not DOWN) and
exactly one "Host unreachable" Alert fires, not one per Service.

## Considered Options

- **Accept the storm (DOWN on unreachable, rejected):** simplest, but
  false-DOWN storms on every observer-side network blip erode trust in Alerts.
- **Self-check then suppress all Alerts (rejected):** avoids the storm but the
  dashboard still shows misleading DOWNs and the operator learns nothing during
  the blind window.

## Consequences

- "Can't tell" and "is dead" are distinct everywhere — Status, debounce, alert
  routing, dashboard, and the future M2 LLM context all reason over UNKNOWN
  explicitly.
- Requires a dedicated Host reachability Probe and observer self-awareness of
  its own connectivity.

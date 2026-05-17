# TCP/port probe

## What to build

A TCP/port-open Probe type for non-HTTP Services (game servers, databases,
etc.), feeding the same Status pipeline — debounce, commit, dashboard, Alerts —
as the HTTP probe. Uses stdlib `net` with an explicit timeout. Independent of
the SSH and Host-gate work; can land any time after the HTTP vertical.

Conforms to `CONTEXT.md` (Probe) and `docs/CONVENTIONS.md` (rules 1, 4).

## Acceptance criteria

- [ ] A Service can be configured with a TCP/port probe (host:port + timeout)
- [ ] Port reachable ⇒ UP; refused/unreachable ⇒ DOWN; same debounce/commit rules
- [ ] TCP Services appear on the dashboard and trigger Alerts like HTTP Services

## Blocked by

- 0002-end-to-end-http-monitoring-vertical

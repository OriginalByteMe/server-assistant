# Rolling Probe-history retention + dashboard sparkline

## What to build

Retain a rolling window of Probe samples (latency, Status transitions) and
render a trend sparkline per Service and Host on the dashboard. Samples older
than the configurable window are pruned so storage cannot grow unbounded.
Storage stays SQLite behind the `Store` seam; a dedicated TSDB attaches later
behind the same seam per ADR 0002 and is explicitly not in scope here.

Conforms to ADR 0002/0006 and `docs/CONVENTIONS.md` (rule 2).

## Acceptance criteria

- [ ] Probe samples retained for a configurable rolling window; older samples pruned
- [ ] Dashboard shows a latency/Status trend sparkline per Service and Host
- [ ] Retention survives restarts; storage does not grow unbounded

## Blocked by

- 0002-end-to-end-http-monitoring-vertical

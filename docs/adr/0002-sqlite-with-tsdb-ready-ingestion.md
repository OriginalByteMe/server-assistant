# SQLite for v1, TSDB-ready ingestion

## Context

Server Assistant must store Services, Hosts, Alerts, and a rolling history of
Probe samples (latency, Status transitions) for trend graphs and future LLM
diagnosis context. The operator does not already run a time-series stack.

## Decision

v1 uses a single SQLite file on the Server Assistant box for all data,
including rolling Probe history. The Probe ingestion pipeline is designed so a
dedicated TSDB (VictoriaMetrics/Prometheus) can be attached behind it later
with no rework — the same "design for the richer path, build the lean one"
principle as ADR 0001.

## Consequences

- Zero datastore ops in v1; trivial backup (one file); fits one box / one
  operator.
- Long-retention / high-cardinality metric graphing is deferred; if metric
  volume or retention needs grow, a TSDB slots in behind the existing ingestion
  rather than replacing the data layer.

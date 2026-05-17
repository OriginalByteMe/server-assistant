-- +goose Up
-- Rolling history of raw Probe outcomes. Runtime/history only (CONVENTIONS
-- rule 6). This is the TSDB-ready ingestion point (ADR 0002); retention
-- pruning is a later issue (ARK-9), deliberately not here. latency_ns is the
-- core.ProbeResult latency in nanoseconds; observed_at is unix milliseconds.
CREATE TABLE probe_samples (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    service     TEXT NOT NULL,
    status      INTEGER NOT NULL,
    latency_ns  INTEGER NOT NULL,
    observed_at INTEGER NOT NULL
);

CREATE INDEX idx_probe_samples_service_time
    ON probe_samples (service, observed_at);

-- +goose Down
DROP TABLE probe_samples;

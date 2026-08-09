-- +goose Up
-- Durable, append-only accountability record for one Harness incident
-- (ADR 0019). A row is written repeatedly in place as its cycle progresses
-- (trigger -> Diagnosis -> Approval -> dispatch -> outcome) rather than one
-- row per transition, but the row itself is never deleted or pruned: this is
-- a separate retention class from probe_samples and is exempt from the
-- rolling Probe-history retention window (ADR 0002 / PruneProbeSamples).
-- trigger_status/mode/approval are the corresponding core enum values;
-- tool_calls/diagnosis are JSON; all *_at columns are unix milliseconds
-- (UTC), 0 meaning "not yet set".
CREATE TABLE harness_cycles (
    id              TEXT PRIMARY KEY,
    subject         TEXT NOT NULL,
    trigger_status  INTEGER NOT NULL,
    mode            INTEGER NOT NULL,
    started_at      INTEGER NOT NULL,
    tool_calls      TEXT NOT NULL,
    diagnosis       TEXT NOT NULL,
    approval        INTEGER NOT NULL,
    approved_by     TEXT NOT NULL DEFAULT '',
    approved_at     INTEGER NOT NULL DEFAULT 0,
    resolved_target TEXT NOT NULL DEFAULT '',
    dispatch_result TEXT NOT NULL DEFAULT '',
    dispatched_at   INTEGER NOT NULL DEFAULT 0,
    outcome         TEXT NOT NULL DEFAULT '',
    outcome_at      INTEGER NOT NULL DEFAULT 0,
    error           TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_harness_cycles_started_at
    ON harness_cycles (started_at DESC);

-- +goose Down
DROP TABLE harness_cycles;

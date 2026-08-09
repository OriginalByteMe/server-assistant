-- +goose Up
-- History for the series where history *is* the signal (GitHub #61): SMART
-- attributes, per-disk/per-share capacity, and array/parity state
-- transitions. Container state, shares and permissions are read on demand
-- and deliberately have no table here.
--
-- One generic table rather than one per series: insert/range-query/prune are
-- identical for all of them, so a per-series table would just be the same
-- three queries copy-pasted three times.
--
--   series      identifies the metric, e.g. "smart.reallocated_sector_ct",
--               "capacity.disk", "array.state" (internal/sampler owns the
--               vocabulary).
--   subject     the device path, share name, or "array".
--   value       the numeric payload for a numeric series (SMART raw value,
--               byte counts). NULL for a textual series or a gap.
--   text_value  the textual payload for a textual series (array state's
--               STARTED/STOPPED/... vocabulary). NULL for a numeric series
--               or a gap.
--   ok          0 when the read was deliberately skipped (disk in standby,
--               GitHub #61) rather than a real measurement. value and
--               text_value are both NULL when ok = 0.
--   note        why, when ok = 0 (e.g. "disk in standby; not woken"). Empty
--               otherwise.
--   sampled_at  unix milliseconds (UTC).
--
-- CONVENTIONS rule 5 (the observer never lies): a skipped read is recorded
-- as an explicit ok = 0 gap row, never omitted and never a fake zero. The
-- trend reader must render that row as a gap, never interpolate across it.
CREATE TABLE metric_samples (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    series      TEXT NOT NULL,
    subject     TEXT NOT NULL,
    value       REAL,
    text_value  TEXT,
    ok          INTEGER NOT NULL DEFAULT 1,
    note        TEXT NOT NULL DEFAULT '',
    sampled_at  INTEGER NOT NULL
);

CREATE INDEX idx_metric_samples_series_subject_time
    ON metric_samples (series, subject, sampled_at);

-- +goose Down
DROP TABLE metric_samples;

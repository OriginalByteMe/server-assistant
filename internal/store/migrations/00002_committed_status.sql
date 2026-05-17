-- +goose Up
-- A Service's last debounce-committed Status. Runtime state only (CONVENTIONS
-- rule 6) — the Service itself is defined in config, never here. status is the
-- core.Status enum value; changed_at is unix milliseconds (UTC).
CREATE TABLE committed_status (
    service    TEXT PRIMARY KEY,
    status     INTEGER NOT NULL,
    changed_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE committed_status;

-- +goose Up
-- HL-SA-18: the script registry, dry-run executor evidence, grant model and
-- proposal lifecycle (GitHub #51, #55). Four tables:
--
--   scripts          content-addressed script bodies. sha256 IS the row's
--                    identity (issue #51: approval binds to a hash, never a
--                    name) — never updated in place, only inserted.
--   proposals        one row per proposal, moving through the explicit
--                    ProposalState machine (internal/scripts/domain.go).
--                    script_hash is mutable: editing a proposal (C3)
--                    repoints it at a new scripts row rather than mutating
--                    the old script body.
--   script_audit_log append-only record of every proposal state transition
--                    — the only way Proposal.State ever changes leaves a
--                    row here (registry.go's transition()).
--   grants           binds to script_hash, never a name (issue #51).
--                    Session grants carry a session_id with a hard TTL
--                    (C2); standing grants do not. Expiry is checked at use
--                    time only (C5) — nothing here sweeps expired rows.
--
-- Timestamps are unix milliseconds, matching every other table in this
-- store (see 00005_smart_samples.sql).
CREATE TABLE scripts (
    sha256      TEXT PRIMARY KEY,
    body        TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE TABLE proposals (
    id                TEXT PRIMARY KEY,
    script_hash       TEXT NOT NULL REFERENCES scripts(sha256),
    state             TEXT NOT NULL,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    dry_run_reasons   TEXT NOT NULL DEFAULT '', -- JSON array of strings
    dry_run_warnings  TEXT NOT NULL DEFAULT '', -- JSON array of strings
    transcript        TEXT NOT NULL DEFAULT '', -- JSON array of TranscriptEntry
    approved_by       TEXT NOT NULL DEFAULT '',
    approved_at       INTEGER NOT NULL DEFAULT 0,
    denied_by         TEXT NOT NULL DEFAULT '',
    denied_at         INTEGER NOT NULL DEFAULT 0,
    deny_reason       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_proposals_state ON proposals (state);
CREATE INDEX idx_proposals_script_hash ON proposals (script_hash);

CREATE TABLE script_audit_log (
    id           TEXT PRIMARY KEY,
    proposal_id  TEXT NOT NULL REFERENCES proposals(id),
    from_state   TEXT NOT NULL,
    to_state     TEXT NOT NULL,
    actor        TEXT NOT NULL,
    reason       TEXT NOT NULL,
    at           INTEGER NOT NULL
);
CREATE INDEX idx_script_audit_proposal_at ON script_audit_log (proposal_id, at);

CREATE TABLE grants (
    id           TEXT PRIMARY KEY,
    script_hash  TEXT NOT NULL,
    scope        TEXT NOT NULL,
    session_id   TEXT NOT NULL DEFAULT '',
    api_key_id   TEXT NOT NULL DEFAULT '',
    granted_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_run_at  INTEGER NOT NULL DEFAULT 0,
    revoked_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_grants_script_hash ON grants (script_hash);

-- +goose Down
DROP TABLE grants;
DROP TABLE script_audit_log;
DROP TABLE proposals;
DROP TABLE scripts;

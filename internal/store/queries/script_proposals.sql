-- name: InsertScriptProposal :exec
INSERT INTO proposals (id, script_hash, state, created_at, updated_at, dry_run_reasons, dry_run_warnings, transcript, approved_by, approved_at, denied_by, denied_at, deny_reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateScriptProposal :exec
UPDATE proposals SET
    script_hash = ?,
    state = ?,
    updated_at = ?,
    dry_run_reasons = ?,
    dry_run_warnings = ?,
    transcript = ?,
    approved_by = ?,
    approved_at = ?,
    denied_by = ?,
    denied_at = ?,
    deny_reason = ?
WHERE id = ?;

-- name: GetScriptProposal :one
SELECT id, script_hash, state, created_at, updated_at, dry_run_reasons, dry_run_warnings, transcript, approved_by, approved_at, denied_by, denied_at, deny_reason
FROM proposals WHERE id = ?;

-- name: ListPendingScriptProposals :many
SELECT id, script_hash, state, created_at, updated_at, dry_run_reasons, dry_run_warnings, transcript, approved_by, approved_at, denied_by, denied_at, deny_reason
FROM proposals WHERE state = 'awaiting_approval' ORDER BY created_at ASC;

-- name: FindApprovedScriptProposalByHash :one
SELECT id, script_hash, state, created_at, updated_at, dry_run_reasons, dry_run_warnings, transcript, approved_by, approved_at, denied_by, denied_at, deny_reason
FROM proposals WHERE script_hash = ? AND state = 'approved'
ORDER BY approved_at DESC LIMIT 1;

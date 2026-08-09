-- name: AppendScriptAudit :exec
INSERT INTO script_audit_log (id, proposal_id, from_state, to_state, actor, reason, at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListScriptAudit :many
SELECT id, proposal_id, from_state, to_state, actor, reason, at
FROM script_audit_log WHERE proposal_id = ? ORDER BY at ASC;

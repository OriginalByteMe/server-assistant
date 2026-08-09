-- name: InsertScriptGrant :exec
INSERT INTO grants (id, script_hash, scope, session_id, api_key_id, granted_at, expires_at, last_run_at, revoked_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetScriptGrant :one
SELECT id, script_hash, scope, session_id, api_key_id, granted_at, expires_at, last_run_at, revoked_at
FROM grants WHERE id = ?;

-- name: ListScriptGrants :many
SELECT id, script_hash, scope, session_id, api_key_id, granted_at, expires_at, last_run_at, revoked_at
FROM grants ORDER BY granted_at DESC;

-- name: RevokeScriptGrant :exec
UPDATE grants SET revoked_at = ? WHERE id = ?;

-- name: TouchScriptGrantLastRun :exec
UPDATE grants SET last_run_at = ? WHERE id = ?;

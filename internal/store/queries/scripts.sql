-- name: UpsertScript :exec
INSERT INTO scripts (sha256, body, created_at)
VALUES (?, ?, ?)
ON CONFLICT(sha256) DO NOTHING;

-- name: GetScript :one
SELECT sha256, body, created_at FROM scripts WHERE sha256 = ?;

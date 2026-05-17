-- name: GetMeta :one
SELECT v FROM app_meta WHERE k = ?;

-- name: SetMeta :exec
INSERT INTO app_meta (k, v) VALUES (?, ?)
ON CONFLICT(k) DO UPDATE SET v = excluded.v;

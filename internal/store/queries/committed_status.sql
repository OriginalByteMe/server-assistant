-- name: UpsertCommittedStatus :exec
INSERT INTO committed_status (service, status, changed_at)
VALUES (?, ?, ?)
ON CONFLICT(service) DO UPDATE SET
    status = excluded.status,
    changed_at = excluded.changed_at;

-- name: ListCommittedStatuses :many
SELECT service, status, changed_at
FROM committed_status
ORDER BY service;

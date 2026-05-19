-- name: InsertProbeSample :exec
INSERT INTO probe_samples (service, status, latency_ns, observed_at)
VALUES (?, ?, ?, ?);

-- name: ListProbeSamples :many
SELECT service, status, latency_ns, observed_at
FROM probe_samples
WHERE service = ?
ORDER BY observed_at ASC, id ASC;

-- name: PruneProbeSamples :exec
DELETE FROM probe_samples
WHERE service = ? AND observed_at < ?;

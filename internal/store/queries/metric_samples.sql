-- name: InsertMetricSample :exec
INSERT INTO metric_samples (series, subject, value, text_value, ok, note, sampled_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListMetricSamples :many
SELECT series, subject, value, text_value, ok, note, sampled_at
FROM metric_samples
WHERE series = ? AND subject = ? AND sampled_at >= ? AND sampled_at <= ?
ORDER BY sampled_at ASC;

-- name: LatestMetricSample :one
SELECT series, subject, value, text_value, ok, note, sampled_at
FROM metric_samples
WHERE series = ? AND subject = ?
ORDER BY sampled_at DESC, id DESC
LIMIT 1;

-- name: PruneMetricSamples :exec
DELETE FROM metric_samples
WHERE sampled_at < ?;

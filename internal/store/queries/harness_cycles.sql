-- name: UpsertHarnessCycle :exec
INSERT INTO harness_cycles (id, subject, trigger_status, mode, started_at, tool_calls, diagnosis, approval, approved_by, approved_at, resolved_target, dispatch_result, dispatched_at, outcome, outcome_at, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    subject = excluded.subject,
    trigger_status = excluded.trigger_status,
    mode = excluded.mode,
    started_at = excluded.started_at,
    tool_calls = excluded.tool_calls,
    diagnosis = excluded.diagnosis,
    approval = excluded.approval,
    approved_by = excluded.approved_by,
    approved_at = excluded.approved_at,
    resolved_target = excluded.resolved_target,
    dispatch_result = excluded.dispatch_result,
    dispatched_at = excluded.dispatched_at,
    outcome = excluded.outcome,
    outcome_at = excluded.outcome_at,
    error = excluded.error;

-- name: ListHarnessCycles :many
SELECT id, subject, trigger_status, mode, started_at, tool_calls, diagnosis, approval, approved_by, approved_at, resolved_target, dispatch_result, dispatched_at, outcome, outcome_at, error
FROM harness_cycles
ORDER BY started_at DESC
LIMIT ?;

-- name: GetHarnessCycle :one
SELECT id, subject, trigger_status, mode, started_at, tool_calls, diagnosis, approval, approved_by, approved_at, resolved_target, dispatch_result, dispatched_at, outcome, outcome_at, error
FROM harness_cycles
WHERE id = ?;

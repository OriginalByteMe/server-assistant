// Package store is the SQLite-backed Store seam. Pure-Go driver
// (modernc.org/sqlite, ADR 0007); migrations are embedded and applied with
// goose. sqlc-generated query code (internal/store/db) is consumed from issue
// 0002 onward and is intentionally not referenced here yet.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver

	"server-assistant/internal/core"
	"server-assistant/internal/store/db"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the SQLite implementation of core.Store.
type Store struct {
	db *sql.DB
	q  *db.Queries
}

var _ core.Store = (*Store)(nil)

// Open opens (creating if absent) the SQLite database at path.
//
// Two settings are load-bearing rather than tuning. Several goroutines write
// concurrently — one per Service poll loop, the Host loop, the harness
// self-probe, and the harness cycle writing its audit record — and with the
// default connection pool they raced into `database is locked (SQLITE_BUSY)`,
// which silently dropped Probe samples and, worse, harness audit writes. An
// audit trail that loses rows under load is not an audit trail (ADR 0019).
//
//   - MaxOpenConns(1) serialises every statement through one connection, so
//     in-process writers queue instead of colliding. SQLite writes are
//     single-writer anyway; this just makes the queue explicit.
//   - WAL plus a busy timeout keeps an external reader (`sqlite3 state.db`,
//     for support) from blocking the daemon.
func Open(_ context.Context, path string) (*Store, error) {
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	sqldb.SetMaxOpenConns(1)
	if err := sqldb.Ping(); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := sqldb.Exec(pragma); err != nil {
			_ = sqldb.Close()
			return nil, fmt.Errorf("apply %q: %w", pragma, err)
		}
	}
	return &Store{db: sqldb, q: db.New(sqldb)}, nil
}

// Migrate applies all embedded goose migrations.
func (s *Store) Migrate(ctx context.Context) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, s.db, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// RecordProbe appends one raw Probe sample to the history.
func (s *Store) RecordProbe(ctx context.Context, p core.ProbeSample) error {
	if err := s.q.InsertProbeSample(ctx, db.InsertProbeSampleParams{
		Service:    p.Service,
		Status:     int64(p.Status),
		LatencyNs:  int64(p.Latency),
		ObservedAt: p.At.UnixMilli(),
	}); err != nil {
		return fmt.Errorf("record probe for %s: %w", p.Service, err)
	}
	return nil
}

// LoadProbeSamples returns up to limit most-recent samples, oldest first.
func (s *Store) LoadProbeSamples(ctx context.Context, service string, limit int) ([]core.ProbeSample, error) {
	rows, err := s.q.ListProbeSamples(ctx, db.ListProbeSamplesParams{Service: service, Limit: int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("load probe samples for %s: %w", service, err)
	}
	out := make([]core.ProbeSample, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		out = append(out, core.ProbeSample{
			Service: r.Service,
			Status:  core.Status(r.Status),
			Latency: time.Duration(r.LatencyNs),
			At:      time.UnixMilli(r.ObservedAt).UTC(),
		})
	}
	return out, nil
}

// PruneProbeSamples deletes a subject's Probe samples older than before,
// enforcing the rolling-retention window so history cannot grow unbounded
// (ADR 0002). Scoped per-subject; uses the (service, observed_at) index.
// NOTE: harness_cycles is a separate retention class (ADR 0019, append-only
// accountability) and is never touched by this method.
func (s *Store) PruneProbeSamples(ctx context.Context, service string, before time.Time) error {
	if err := s.q.PruneProbeSamples(ctx, db.PruneProbeSamplesParams{
		Service:    service,
		ObservedAt: before.UnixMilli(),
	}); err != nil {
		return fmt.Errorf("prune probe samples for %s: %w", service, err)
	}
	return nil
}

// SaveCommittedStatus upserts a Service's latest committed Status.
func (s *Store) SaveCommittedStatus(ctx context.Context, cs core.CommittedStatus) error {
	if err := s.q.UpsertCommittedStatus(ctx, db.UpsertCommittedStatusParams{
		Service:   cs.Service,
		Status:    int64(cs.Status),
		ChangedAt: cs.ChangedAt.UnixMilli(),
	}); err != nil {
		return fmt.Errorf("save committed status for %s: %w", cs.Service, err)
	}
	return nil
}

// LoadCommittedStatuses returns every Service's last committed Status so the
// daemon resumes across restarts without re-deriving from UNKNOWN.
func (s *Store) LoadCommittedStatuses(ctx context.Context) ([]core.CommittedStatus, error) {
	rows, err := s.q.ListCommittedStatuses(ctx)
	if err != nil {
		return nil, fmt.Errorf("load committed statuses: %w", err)
	}
	out := make([]core.CommittedStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, core.CommittedStatus{
			Service:   r.Service,
			Status:    core.Status(r.Status),
			ChangedAt: time.UnixMilli(r.ChangedAt).UTC(),
		})
	}
	return out, nil
}

// SaveHarnessCycle upserts a Harness cycle record. A cycle may be written
// repeatedly in place as it progresses (trigger -> Diagnosis -> Approval ->
// dispatch -> outcome), updating all mutable fields. Rows are append-only in
// the sense that they are never deleted or pruned (ADR 0019).
func (s *Store) SaveHarnessCycle(ctx context.Context, c core.HarnessCycle) error {
	toolCallsJSON, err := json.Marshal(c.ToolCalls)
	if err != nil {
		return fmt.Errorf("marshal tool calls: %w", err)
	}
	diagnosisJSON, err := json.Marshal(c.Diagnosis)
	if err != nil {
		return fmt.Errorf("marshal diagnosis: %w", err)
	}
	if err := s.q.UpsertHarnessCycle(ctx, db.UpsertHarnessCycleParams{
		ID:             c.ID,
		Subject:        c.Subject,
		TriggerStatus:  int64(c.TriggerStatus),
		Mode:           int64(c.Mode),
		StartedAt:      millisFromTime(c.StartedAt),
		ToolCalls:      string(toolCallsJSON),
		Diagnosis:      string(diagnosisJSON),
		Approval:       int64(c.Approval),
		ApprovedBy:     c.ApprovedBy,
		ApprovedAt:     millisFromTime(c.ApprovedAt),
		ResolvedTarget: c.ResolvedTarget,
		DispatchResult: c.DispatchResult,
		DispatchedAt:   millisFromTime(c.DispatchedAt),
		Outcome:        c.Outcome,
		OutcomeAt:      millisFromTime(c.OutcomeAt),
		Error:          c.Error,
	}); err != nil {
		return fmt.Errorf("save harness cycle %s: %w", c.ID, err)
	}
	return nil
}

// ListHarnessCycles returns up to limit most-recent Harness cycles, newest
// first (by started_at DESC).
func (s *Store) ListHarnessCycles(ctx context.Context, limit int) ([]core.HarnessCycle, error) {
	rows, err := s.q.ListHarnessCycles(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("list harness cycles: %w", err)
	}
	out := make([]core.HarnessCycle, 0, len(rows))
	for _, r := range rows {
		var toolCalls []core.ToolCall
		if err := json.Unmarshal([]byte(r.ToolCalls), &toolCalls); err != nil {
			return nil, fmt.Errorf("unmarshal tool calls for cycle %s: %w", r.ID, err)
		}
		var diagnosis core.Diagnosis
		if err := json.Unmarshal([]byte(r.Diagnosis), &diagnosis); err != nil {
			return nil, fmt.Errorf("unmarshal diagnosis for cycle %s: %w", r.ID, err)
		}
		out = append(out, core.HarnessCycle{
			ID:             r.ID,
			Subject:        r.Subject,
			TriggerStatus:  core.Status(r.TriggerStatus),
			Mode:           core.HarnessMode(r.Mode),
			StartedAt:      timeFromMillis(r.StartedAt),
			ToolCalls:      toolCalls,
			Diagnosis:      diagnosis,
			Approval:       core.ApprovalDecision(r.Approval),
			ApprovedBy:     r.ApprovedBy,
			ApprovedAt:     timeFromMillis(r.ApprovedAt),
			ResolvedTarget: r.ResolvedTarget,
			DispatchResult: r.DispatchResult,
			DispatchedAt:   timeFromMillis(r.DispatchedAt),
			Outcome:        r.Outcome,
			OutcomeAt:      timeFromMillis(r.OutcomeAt),
			Error:          r.Error,
		})
	}
	return out, nil
}

// GetHarnessCycle returns a single Harness cycle by id. Returns an error if
// not found.
func (s *Store) GetHarnessCycle(ctx context.Context, id string) (core.HarnessCycle, error) {
	r, err := s.q.GetHarnessCycle(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.HarnessCycle{}, fmt.Errorf("harness cycle %s: not found", id)
		}
		return core.HarnessCycle{}, fmt.Errorf("get harness cycle %s: %w", id, err)
	}
	var toolCalls []core.ToolCall
	if err := json.Unmarshal([]byte(r.ToolCalls), &toolCalls); err != nil {
		return core.HarnessCycle{}, fmt.Errorf("unmarshal tool calls for cycle %s: %w", id, err)
	}
	var diagnosis core.Diagnosis
	if err := json.Unmarshal([]byte(r.Diagnosis), &diagnosis); err != nil {
		return core.HarnessCycle{}, fmt.Errorf("unmarshal diagnosis for cycle %s: %w", id, err)
	}
	return core.HarnessCycle{
		ID:             r.ID,
		Subject:        r.Subject,
		TriggerStatus:  core.Status(r.TriggerStatus),
		Mode:           core.HarnessMode(r.Mode),
		StartedAt:      timeFromMillis(r.StartedAt),
		ToolCalls:      toolCalls,
		Diagnosis:      diagnosis,
		Approval:       core.ApprovalDecision(r.Approval),
		ApprovedBy:     r.ApprovedBy,
		ApprovedAt:     timeFromMillis(r.ApprovedAt),
		ResolvedTarget: r.ResolvedTarget,
		DispatchResult: r.DispatchResult,
		DispatchedAt:   timeFromMillis(r.DispatchedAt),
		Outcome:        r.Outcome,
		OutcomeAt:      timeFromMillis(r.OutcomeAt),
		Error:          r.Error,
	}, nil
}

// millisFromTime converts a time.Time to unix milliseconds, returning 0 for
// the zero time value to maintain the convention that 0 means "not yet set".
func millisFromTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// timeFromMillis converts unix milliseconds back to time.Time, returning the
// zero time value for 0 milliseconds.
func timeFromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Package store is the SQLite-backed Store seam. Pure-Go driver
// (modernc.org/sqlite, ADR 0007); migrations are embedded and applied with
// goose. sqlc-generated query code (internal/store/db) is consumed from issue
// 0002 onward and is intentionally not referenced here yet.
package store

import (
	"context"
	"database/sql"
	"embed"
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
func Open(_ context.Context, path string) (*Store, error) {
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := sqldb.Ping(); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
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

// LoadProbeSamples returns a Service's recorded Probe samples, oldest first.
func (s *Store) LoadProbeSamples(ctx context.Context, service string) ([]core.ProbeSample, error) {
	rows, err := s.q.ListProbeSamples(ctx, service)
	if err != nil {
		return nil, fmt.Errorf("load probe samples for %s: %w", service, err)
	}
	out := make([]core.ProbeSample, 0, len(rows))
	for _, r := range rows {
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

func (s *Store) Close() error {
	return s.db.Close()
}

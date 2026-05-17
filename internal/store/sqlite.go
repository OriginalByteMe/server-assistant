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

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver

	"server-assistant/internal/core"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the SQLite implementation of core.Store.
type Store struct {
	db *sql.DB
}

var _ core.Store = (*Store)(nil)

// Open opens (creating if absent) the SQLite database at path.
func Open(_ context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	return &Store{db: db}, nil
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

func (s *Store) Close() error {
	return s.db.Close()
}

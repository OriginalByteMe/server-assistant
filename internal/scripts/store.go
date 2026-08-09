package scripts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver

	"server-assistant/internal/store/db"
)

// sqlStore is the production Store, backed by sqlc queries against
// internal/store/migrations/00006_scripts.sql. It opens its own *sql.DB
// handle to the same SQLite file the daemon's main store.Store already
// migrates (00001-00006 all live in internal/store/migrations, applied by
// whichever store.Store.Migrate(ctx) call runs first — see this ticket's
// Current state for the exact main.go ordering). A second connection to
// the same WAL-mode file is safe; MaxOpenConns(1) mirrors
// internal/store/sqlite.go's rationale for why a single serialized writer
// matters here too (an audit trail that loses rows under load is not an
// audit trail).
type sqlStore struct {
	conn *sql.DB
	q    *db.Queries
}

// NewStore opens (without migrating — the caller must already have run
// store.Store.Migrate(ctx) against this same path) a Store for path.
func NewStore(_ context.Context, path string) (*sqlStore, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("scripts: open sqlite %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1)
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("scripts: ping sqlite %s: %w", path, err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := conn.Exec(pragma); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("scripts: apply %q: %w", pragma, err)
		}
	}
	return &sqlStore{conn: conn, q: db.New(conn)}, nil
}

func (s *sqlStore) Close() error { return s.conn.Close() }

var _ Store = (*sqlStore)(nil)

// --- scripts ---

func (s *sqlStore) UpsertScript(ctx context.Context, sc Script) error {
	return s.q.UpsertScript(ctx, db.UpsertScriptParams{
		Sha256:    sc.SHA256,
		Body:      sc.Body,
		CreatedAt: millis(sc.CreatedAt),
	})
}

func (s *sqlStore) GetScript(ctx context.Context, hash string) (Script, error) {
	row, err := s.q.GetScript(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return Script{}, ErrScriptNotFound
	}
	if err != nil {
		return Script{}, err
	}
	return Script{SHA256: row.Sha256, Body: row.Body, CreatedAt: fromMillis(row.CreatedAt)}, nil
}

// --- proposals ---

func (s *sqlStore) InsertProposal(ctx context.Context, p Proposal) error {
	params, err := proposalToInsertParams(p)
	if err != nil {
		return err
	}
	return s.q.InsertScriptProposal(ctx, params)
}

func (s *sqlStore) UpdateProposal(ctx context.Context, p Proposal) error {
	params, err := proposalToUpdateParams(p)
	if err != nil {
		return err
	}
	return s.q.UpdateScriptProposal(ctx, params)
}

func (s *sqlStore) GetProposal(ctx context.Context, id string) (Proposal, error) {
	row, err := s.q.GetScriptProposal(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, ErrProposalNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	return proposalFromRow(row)
}

func (s *sqlStore) ListPendingProposals(ctx context.Context) ([]Proposal, error) {
	rows, err := s.q.ListPendingScriptProposals(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Proposal, 0, len(rows))
	for _, row := range rows {
		p, err := proposalFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *sqlStore) FindApprovedProposalByHash(ctx context.Context, hash string) (Proposal, bool, error) {
	row, err := s.q.FindApprovedScriptProposalByHash(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, false, nil
	}
	if err != nil {
		return Proposal{}, false, err
	}
	p, err := proposalFromRow(row)
	return p, true, err
}

// --- audit ---

func (s *sqlStore) AppendAudit(ctx context.Context, e AuditEntry) error {
	return s.q.AppendScriptAudit(ctx, db.AppendScriptAuditParams{
		ID:         e.ID,
		ProposalID: e.ProposalID,
		FromState:  string(e.FromState),
		ToState:    string(e.ToState),
		Actor:      e.Actor,
		Reason:     e.Reason,
		At:         millis(e.At),
	})
}

func (s *sqlStore) ListAudit(ctx context.Context, proposalID string) ([]AuditEntry, error) {
	rows, err := s.q.ListScriptAudit(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	out := make([]AuditEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, AuditEntry{
			ID:         row.ID,
			ProposalID: row.ProposalID,
			FromState:  ProposalState(row.FromState),
			ToState:    ProposalState(row.ToState),
			Actor:      row.Actor,
			Reason:     row.Reason,
			At:         fromMillis(row.At),
		})
	}
	return out, nil
}

// --- grants ---

func (s *sqlStore) InsertGrant(ctx context.Context, g Grant) error {
	return s.q.InsertScriptGrant(ctx, db.InsertScriptGrantParams{
		ID:         g.ID,
		ScriptHash: g.ScriptHash,
		Scope:      string(g.Scope),
		SessionID:  g.SessionID,
		ApiKeyID:   g.APIKeyID,
		GrantedAt:  millis(g.GrantedAt),
		ExpiresAt:  millis(g.ExpiresAt),
		LastRunAt:  millis(g.LastRunAt),
		RevokedAt:  millis(g.RevokedAt),
	})
}

func (s *sqlStore) GetGrant(ctx context.Context, id string) (Grant, error) {
	row, err := s.q.GetScriptGrant(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrGrantNotFound
	}
	if err != nil {
		return Grant{}, err
	}
	return grantFromRow(row), nil
}

func (s *sqlStore) ListGrants(ctx context.Context) ([]Grant, error) {
	rows, err := s.q.ListScriptGrants(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Grant, 0, len(rows))
	for _, row := range rows {
		out = append(out, grantFromRow(row))
	}
	return out, nil
}

func (s *sqlStore) RevokeGrant(ctx context.Context, id string, at time.Time) error {
	return s.q.RevokeScriptGrant(ctx, db.RevokeScriptGrantParams{ID: id, RevokedAt: millis(at)})
}

func (s *sqlStore) TouchGrantLastRun(ctx context.Context, id string, at time.Time) error {
	return s.q.TouchScriptGrantLastRun(ctx, db.TouchScriptGrantLastRunParams{ID: id, LastRunAt: millis(at)})
}

// --- conversions ---

func proposalToInsertParams(p Proposal) (db.InsertScriptProposalParams, error) {
	reasons, warnings, transcript, err := marshalProposalBlobs(p)
	if err != nil {
		return db.InsertScriptProposalParams{}, err
	}
	return db.InsertScriptProposalParams{
		ID: p.ID, ScriptHash: p.ScriptHash, State: string(p.State),
		CreatedAt: millis(p.CreatedAt), UpdatedAt: millis(p.UpdatedAt),
		DryRunReasons: reasons, DryRunWarnings: warnings, Transcript: transcript,
		ApprovedBy: p.ApprovedBy, ApprovedAt: millis(p.ApprovedAt),
		DeniedBy: p.DeniedBy, DeniedAt: millis(p.DeniedAt), DenyReason: p.DenyReason,
	}, nil
}

func proposalToUpdateParams(p Proposal) (db.UpdateScriptProposalParams, error) {
	reasons, warnings, transcript, err := marshalProposalBlobs(p)
	if err != nil {
		return db.UpdateScriptProposalParams{}, err
	}
	return db.UpdateScriptProposalParams{
		ID: p.ID, ScriptHash: p.ScriptHash, State: string(p.State), UpdatedAt: millis(p.UpdatedAt),
		DryRunReasons: reasons, DryRunWarnings: warnings, Transcript: transcript,
		ApprovedBy: p.ApprovedBy, ApprovedAt: millis(p.ApprovedAt),
		DeniedBy: p.DeniedBy, DeniedAt: millis(p.DeniedAt), DenyReason: p.DenyReason,
	}, nil
}

func marshalProposalBlobs(p Proposal) (reasons, warnings, transcript string, err error) {
	r, err := json.Marshal(nonNil(p.RejectReasons))
	if err != nil {
		return "", "", "", fmt.Errorf("scripts: marshal reasons: %w", err)
	}
	w, err := json.Marshal(nonNil(p.Warnings))
	if err != nil {
		return "", "", "", fmt.Errorf("scripts: marshal warnings: %w", err)
	}
	t, err := json.Marshal(p.Transcript)
	if err != nil {
		return "", "", "", fmt.Errorf("scripts: marshal transcript: %w", err)
	}
	return string(r), string(w), string(t), nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func proposalFromRow(row db.Proposal) (Proposal, error) {
	var reasons, warnings []string
	var transcript []TranscriptEntry
	if row.DryRunReasons != "" {
		if err := json.Unmarshal([]byte(row.DryRunReasons), &reasons); err != nil {
			return Proposal{}, fmt.Errorf("scripts: unmarshal reasons: %w", err)
		}
	}
	if row.DryRunWarnings != "" {
		if err := json.Unmarshal([]byte(row.DryRunWarnings), &warnings); err != nil {
			return Proposal{}, fmt.Errorf("scripts: unmarshal warnings: %w", err)
		}
	}
	if row.Transcript != "" {
		if err := json.Unmarshal([]byte(row.Transcript), &transcript); err != nil {
			return Proposal{}, fmt.Errorf("scripts: unmarshal transcript: %w", err)
		}
	}
	return Proposal{
		ID: row.ID, ScriptHash: row.ScriptHash, State: ProposalState(row.State),
		CreatedAt: fromMillis(row.CreatedAt), UpdatedAt: fromMillis(row.UpdatedAt),
		RejectReasons: reasons, Warnings: warnings, Transcript: transcript,
		ApprovedBy: row.ApprovedBy, ApprovedAt: fromMillis(row.ApprovedAt),
		DeniedBy: row.DeniedBy, DeniedAt: fromMillis(row.DeniedAt), DenyReason: row.DenyReason,
	}, nil
}

func grantFromRow(row db.Grant) Grant {
	return Grant{
		ID: row.ID, ScriptHash: row.ScriptHash, Scope: GrantScope(row.Scope),
		SessionID: row.SessionID, APIKeyID: row.ApiKeyID,
		GrantedAt: fromMillis(row.GrantedAt), ExpiresAt: fromMillis(row.ExpiresAt),
		LastRunAt: fromMillis(row.LastRunAt), RevokedAt: fromMillis(row.RevokedAt),
	}
}

// millis/fromMillis mirror internal/store/sqlite.go's convention: 0 means
// "not yet set" (a zero time.Time), never a fake real timestamp.
func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

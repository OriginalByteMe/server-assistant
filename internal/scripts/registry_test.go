package scripts

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/store"
)

// newTestRegistry opens a fresh, fully-migrated SQLite file (the same
// pattern internal/store/harness_test.go uses: a per-test temp file, not a
// literal :memory: DSN, so the two connections below — one that runs the
// shared goose migrations, one that runs this package's queries — see the
// same schema) and returns a Registry wired to a real Executor.
func newTestRegistry(t *testing.T) (*Registry, *sqlStore) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "scripts.db")

	st, err := store.Open(ctx, dbPath)
	require.NoError(t, err)
	require.NoError(t, st.Migrate(ctx))
	t.Cleanup(func() { _ = st.Close() })

	sstore, err := NewStore(ctx, dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sstore.Close() })

	// A real, existing temp dir stands in for /boot: production defaults
	// to /boot, but this host's /boot (if any) is not guaranteed bindable
	// in a test sandbox, and the guard mechanism itself is already proven
	// against /boot semantics in sandbox_test.go.
	exec := &Executor{
		DryRunTimeout:  10 * time.Second,
		RunTimeout:     10 * time.Second,
		ProtectedPaths: []string{t.TempDir()},
	}
	return NewRegistry(sstore, exec, nil), sstore
}

func TestRegistry_FullLifecycleHappyPath(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	p, err := r.Propose(ctx, "#!/bin/bash\necho hi\nexit 0\n", "llm")
	require.NoError(t, err)
	require.Equal(t, StateAwaitingApproval, p.State, "reasons: %v", p.RejectReasons)

	approved, err := r.Approve(ctx, p.ID, "noah")
	require.NoError(t, err)
	require.Equal(t, StateApproved, approved.State)

	final, result, err := r.Execute(ctx, p.ID, "noah")
	require.NoError(t, err)
	require.True(t, result.Succeeded)
	require.Equal(t, StateSucceeded, final.State)

	audit, err := r.AuditLog(ctx, p.ID)
	require.NoError(t, err)
	// proposed -> dry_run_ok -> awaiting_approval -> approved -> running -> succeeded
	require.GreaterOrEqual(t, len(audit), 6)
}

func TestRegistry_DryRunFailureBlocksApproval(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	p, err := r.Propose(ctx, "#!/bin/bash\nexit 1\n", "llm")
	require.NoError(t, err)
	require.Equal(t, StateDryRunFailed, p.State)

	// Not proven by comment: Approve is attempted directly and must fail,
	// because the state machine has no edge from dry_run_failed to
	// approved no matter what Approve does internally.
	_, err = r.Approve(ctx, p.ID, "noah")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDryRunRequired))

	got, err := r.GetProposal(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, StateDryRunFailed, got.State, "a failed dry run must never reach approved")
}

func TestRegistry_EditInvalidatesGrant(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	original := "#!/bin/bash\necho original\nexit 0\n"
	p, err := r.Propose(ctx, original, "llm")
	require.NoError(t, err)
	require.Equal(t, StateAwaitingApproval, p.State)

	approved, err := r.Approve(ctx, p.ID, "noah")
	require.NoError(t, err)
	originalHash := approved.ScriptHash

	grant, err := r.IssueGrant(ctx, originalHash, ScopeStanding, "key-1", "", GrantTTLs{})
	require.NoError(t, err)

	// Sanity: the grant is valid before any edit.
	_, err = r.CheckGrant(ctx, grant.ID, time.Now())
	require.NoError(t, err)

	// One byte changed.
	edited := "#!/bin/bash\necho originaL\nexit 0\n"
	require.NotEqual(t, original, edited)
	editedProposal, err := r.Edit(ctx, p.ID, edited, "noah")
	require.NoError(t, err)
	require.NotEqual(t, originalHash, editedProposal.ScriptHash, "editing one byte must change the content hash")
	require.Equal(t, StateAwaitingApproval, editedProposal.State, "reasons: %v", editedProposal.RejectReasons)

	// The OLD grant (still bound to originalHash) must now be invalid:
	// no proposal is "approved" for that hash any more.
	_, err = r.CheckGrant(ctx, grant.ID, time.Now())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrGrantNotApproved), "got %v", err)
}

func TestRegistry_PreconditionMismatchFailsClosed(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()

	p, err := r.Propose(ctx, "#!/bin/bash\necho hi\n", "llm")
	require.NoError(t, err)
	approved, err := r.Approve(ctx, p.ID, "noah")
	require.NoError(t, err)

	r.precond = preconditionFunc(func(context.Context, string) error {
		return errors.New("named container no longer exists")
	})

	final, result, err := r.Execute(ctx, approved.ID, "noah")
	require.Nil(t, result, "a precondition failure must never execute the script")
	require.True(t, errors.Is(err, ErrPreconditionFailed))
	// Fails closed to pending: the grant survives, the run does not.
	require.Equal(t, StateAwaitingApproval, final.State)

	// The proposal can still be approved and run again once preconditions
	// hold (grant/approval survived the failure).
	r.precond = nil
	reapproved, err := r.Approve(ctx, approved.ID, "noah")
	require.NoError(t, err)
	require.Equal(t, StateApproved, reapproved.State)
}

type preconditionFunc func(ctx context.Context, body string) error

func (f preconditionFunc) Check(ctx context.Context, body string) error { return f(ctx, body) }

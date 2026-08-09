package scripts

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCheck_IsNotAHashOracle is C6's highest-risk item: check(text) must
// return byte-identical output for text that happens to match an existing
// approved+granted script and for text that does not, so an LLM can never
// learn "a script matching this exact text already exists/is granted" by
// diffing two check() calls.
func TestCheck_IsNotAHashOracle(t *testing.T) {
	ctx := context.Background()
	exec := &Executor{DryRunTimeout: 10 * time.Second}
	text := "#!/bin/bash\necho hi\nexit 0\n"

	// Baseline: check() against an empty registry (nothing stored anywhere).
	baseline, err := Check(ctx, exec, text)
	require.NoError(t, err)

	// Now build a REAL registry, propose+approve+grant the EXACT same text,
	// and check() it again through the same Executor.
	r, _ := newTestRegistry(t)
	p, err := r.Propose(ctx, text, "llm")
	require.NoError(t, err)
	approved, err := r.Approve(ctx, p.ID, "noah")
	require.NoError(t, err)
	_, err = r.IssueGrant(ctx, approved.ScriptHash, ScopeStanding, "key-1", "", GrantTTLs{})
	require.NoError(t, err)

	withStoredMatch, err := Check(ctx, exec, text)
	require.NoError(t, err)

	require.Equal(t, baseline, withStoredMatch,
		"check(text) must be identical whether or not this exact text is already an approved, granted script — "+
			"otherwise it is a hash oracle over the grant store (C6)")

	// A DIFFERENT text, never stored anywhere, gets its own independent
	// verdict driven only by its own dry run — not "not found, therefore
	// automatically valid/invalid".
	other, err := Check(ctx, exec, "#!/bin/bash\nexit 1\n")
	require.NoError(t, err)
	require.NotEqual(t, baseline, other, "a different script's own dry run must drive its own verdict")
}

// TestCheck_NeverTouchesStore is the structural half of the guarantee:
// Check's signature takes no Store/Registry at all, so there is no code
// path by which it could consult one. This test exists to fail loudly if a
// future change adds a store parameter to Check without a second look.
func TestCheck_NeverTouchesStore(t *testing.T) {
	// Compile-time-ish assertion, made explicit: Check's only inputs are a
	// context, an Executor (which never persists anything — sandbox.go's
	// DryRun/Run never touch a Store), and the submitted text.
	var _ func(context.Context, *Executor, string) (CheckResult, error) = Check
}

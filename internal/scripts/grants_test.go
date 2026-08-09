package scripts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func approvedHash(t *testing.T, r *Registry, ctx context.Context, body string) string {
	t.Helper()
	p, err := r.Propose(ctx, body, "llm")
	require.NoError(t, err)
	approved, err := r.Approve(ctx, p.ID, "noah")
	require.NoError(t, err)
	return approved.ScriptHash
}

func TestRegistry_SessionGrantExpiresAtTTL(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	hash := approvedHash(t, r, ctx, "#!/bin/bash\necho hi\n")

	grant, err := r.IssueGrant(ctx, hash, ScopeSession, "key-1", "session-abc", GrantTTLs{Session: time.Hour})
	require.NoError(t, err)

	// Not yet expired.
	_, err = r.CheckGrant(ctx, grant.ID, grant.GrantedAt.Add(59*time.Minute))
	require.NoError(t, err)

	// At/after the TTL boundary, expired — checked at use time, not swept.
	_, err = r.CheckGrant(ctx, grant.ID, grant.GrantedAt.Add(time.Hour))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrGrantExpired))
}

func TestRegistry_SessionGrantRequiresSessionID(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	hash := approvedHash(t, r, ctx, "#!/bin/bash\necho hi\n")

	_, err := r.IssueGrant(ctx, hash, ScopeSession, "key-1", "", GrantTTLs{})
	require.Error(t, err, "a session grant with no server-issued session id is not mechanically checkable (C2)")
}

func TestRegistry_RevocationIsInstant(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	hash := approvedHash(t, r, ctx, "#!/bin/bash\necho hi\n")

	grant, err := r.IssueGrant(ctx, hash, ScopeStanding, "key-1", "", GrantTTLs{Standing: 90 * 24 * time.Hour})
	require.NoError(t, err)

	_, err = r.CheckGrant(ctx, grant.ID, time.Now())
	require.NoError(t, err)

	require.NoError(t, r.RevokeGrant(ctx, grant.ID, "noah"))

	_, err = r.CheckGrant(ctx, grant.ID, time.Now())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrGrantRevoked))
}

func TestRegistry_ListGrants(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	hash := approvedHash(t, r, ctx, "#!/bin/bash\necho hi\n")

	_, err := r.IssueGrant(ctx, hash, ScopeStanding, "key-1", "", GrantTTLs{})
	require.NoError(t, err)
	_, err = r.IssueGrant(ctx, hash, ScopeStanding, "key-2", "", GrantTTLs{})
	require.NoError(t, err)

	grants, err := r.ListGrants(ctx)
	require.NoError(t, err)
	require.Len(t, grants, 2)
}

package scripts

import (
	"context"
	"fmt"
	"time"
)

// GrantTTLs bounds session and standing grant lifetimes (coordinator
// decision C5, GitHub #55: session 4h, standing 90d, both config-driven).
type GrantTTLs struct {
	Session  time.Duration
	Standing time.Duration
}

func (t GrantTTLs) forScope(scope GrantScope) time.Duration {
	if scope == ScopeStanding {
		if t.Standing > 0 {
			return t.Standing
		}
		return 90 * 24 * time.Hour
	}
	if t.Session > 0 {
		return t.Session
	}
	return 4 * time.Hour
}

// IssueGrant creates a Grant bound to scriptHash. A session grant requires a
// sessionID (C2: a "session" for a stateless MCP caller is an API key plus a
// server-issued session id with a hard TTL, never the TCP connection —
// enforced here by requiring the caller to supply one rather than inferring
// anything from the transport).
func (r *Registry) IssueGrant(ctx context.Context, scriptHash string, scope GrantScope, apiKeyID, sessionID string, ttls GrantTTLs) (Grant, error) {
	if scope == ScopeSession && sessionID == "" {
		return Grant{}, fmt.Errorf("scripts: session grant requires a sessionID")
	}
	now := r.now()
	g := Grant{
		ID:         r.newID(),
		ScriptHash: scriptHash,
		Scope:      scope,
		SessionID:  sessionID,
		APIKeyID:   apiKeyID,
		GrantedAt:  now,
		ExpiresAt:  now.Add(ttls.forScope(scope)),
	}
	if err := r.store.InsertGrant(ctx, g); err != nil {
		return Grant{}, fmt.Errorf("scripts: store grant: %w", err)
	}
	return g, nil
}

// CheckGrant validates a grant at use time (C5: expiry is checked at use
// time, never assumed swept). A grant is usable only when:
//   - it is not expired and not revoked, AND
//   - an approved Proposal currently exists whose ScriptHash matches the
//     grant's ScriptHash exactly.
//
// That second condition is the whole mechanism behind "editing an approved
// script invalidates its grant" (C1-C6): Registry.Edit repoints the
// proposal at a new hash and takes the proposal out of "approved" for the
// old one, so this lookup starts failing for a grant that still names the
// old hash — no code here has to notice the edit happened, or touch the
// grant row at all.
func (r *Registry) CheckGrant(ctx context.Context, grantID string, now time.Time) (Grant, error) {
	g, err := r.store.GetGrant(ctx, grantID)
	if err != nil {
		return Grant{}, err
	}
	if g.Revoked() {
		return Grant{}, ErrGrantRevoked
	}
	if g.Expired(now) {
		return Grant{}, ErrGrantExpired
	}
	if _, ok, err := r.store.FindApprovedProposalByHash(ctx, g.ScriptHash); err != nil {
		return Grant{}, err
	} else if !ok {
		return Grant{}, ErrGrantNotApproved
	}
	return g, nil
}

// TouchGrant records a successful use for observability (the revocation UI
// shows last-run, per C5).
func (r *Registry) TouchGrant(ctx context.Context, grantID string, at time.Time) error {
	return r.store.TouchGrantLastRun(ctx, grantID, at)
}

// RevokeGrant is instant (C5): it flips RevokedAt and nothing else — the
// very next CheckGrant call sees it.
func (r *Registry) RevokeGrant(ctx context.Context, grantID, actor string) error {
	return r.store.RevokeGrant(ctx, grantID, r.now())
}

// ListGrants returns every grant, for the revocation UI (C5: name, hash
// prefix, granted-when, last-run, scope).
func (r *Registry) ListGrants(ctx context.Context) ([]Grant, error) {
	return r.store.ListGrants(ctx)
}

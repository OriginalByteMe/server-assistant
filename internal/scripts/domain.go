// Package scripts implements HL-SA-18: the script registry, the production
// dry-run executor, the grant model and the proposal lifecycle described in
// GitHub issues #51 and #55.
//
// The whole package rests on one fact: approval binds to a content hash,
// never a name (issue #51). A Script is therefore content-addressed — its
// ID is its own SHA-256 — and a Proposal's identity as "the same script" is
// entirely a function of that hash. Edit one byte and it is, mechanically,
// a different script that must earn its own dry run and its own approval.
package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Sentinel errors. Every rejection a caller needs to branch on is one of
// these, wrapped with %w so errors.Is keeps working through the registry.
var (
	// ErrInvalidTransition reports a proposal state change nothing in this
	// package requests explicitly — CONVENTIONS "explicit over magic": there
	// is no implicit state change, so an unlisted transition is a bug, not a
	// no-op.
	ErrInvalidTransition = errors.New("scripts: invalid proposal state transition")
	// ErrDryRunRequired is returned when Approve is attempted on a proposal
	// that never reached dry_run_ok. This is the mechanical enforcement of
	// "a script with no working dry run cannot be approved" (issue #51) —
	// the state machine refuses the transition, it is not a convention the
	// caller is trusted to honour.
	ErrDryRunRequired    = errors.New("scripts: a script with no working dry run cannot be approved")
	ErrArgumentsRejected = errors.New("scripts: scripts never receive arguments")
	ErrBootWriteRejected = errors.New("scripts: refused: script targeted a protected path (/boot)")
	// ErrSandboxUnavailable is the honest-ceiling outcome (FINDINGS.md) when
	// this host cannot provide the unshare/mount-namespace guarantees the
	// executor depends on. It is surfaced as a dry-run rejection reason, not
	// swallowed into a false APPROVED-FOR-REVIEW.
	ErrSandboxUnavailable = errors.New("scripts: dry-run sandbox unavailable on this host")
	ErrGrantExpired       = errors.New("scripts: grant expired")
	ErrGrantRevoked       = errors.New("scripts: grant revoked")
	// ErrGrantNotApproved is returned when a grant's script hash no longer
	// has an approved proposal — the mechanism behind "editing an approved
	// script invalidates its grant" (issue #55 C1-C6): a grant binds to a
	// hash, and once that hash stops being the hash of an approved
	// proposal, the grant is inert without anyone having to touch it.
	ErrGrantNotApproved   = errors.New("scripts: no approved proposal exists for this script hash")
	ErrPreconditionFailed = errors.New("scripts: preconditions no longer hold")
	ErrProposalNotFound   = errors.New("scripts: proposal not found")
	ErrScriptNotFound     = errors.New("scripts: script not found")
	ErrGrantNotFound      = errors.New("scripts: grant not found")
	// ErrProtectedPathGuardFailed reports that a real run's protected-path
	// (e.g. /boot) bind-mount-read-only guard itself could not be
	// established — the script never ran. Distinct from the script's own
	// exit code: this is a sandbox failure, never downgraded to a plain
	// RunResult{Succeeded:false}.
	ErrProtectedPathGuardFailed = errors.New("scripts: failed to establish protected-path read-only guard; script did not run")
)

// Script is one immutable, content-addressed script body. There is no
// mutable "name" identity anywhere in this package on purpose — Unraid's
// user.scripts directory is never reused as storage (coordinator decision
// C1, GitHub #55): sharing it would let Unraid's own UI edit an approved
// script out from under its hash.
type Script struct {
	SHA256    string
	Body      string
	CreatedAt time.Time
}

// hashScript is the one place a script's identity is computed.
func hashScript(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// NewScript hashes body and stamps CreatedAt.
func NewScript(body string, now time.Time) Script {
	return Script{SHA256: hashScript(body), Body: body, CreatedAt: now}
}

// ProposalState is one point in the explicit lifecycle:
//
//	proposed -> dry_run_ok | dry_run_failed -> awaiting_approval
//	         -> approved | denied | expired
//	         -> running -> succeeded | failed | precondition_failed
//
// Every arrow is listed in transitions below; there is no other way to move
// between states (CONVENTIONS rule 3, "explicit over magic").
type ProposalState string

const (
	StateProposed           ProposalState = "proposed"
	StateDryRunOK           ProposalState = "dry_run_ok"
	StateDryRunFailed       ProposalState = "dry_run_failed"
	StateAwaitingApproval   ProposalState = "awaiting_approval"
	StateApproved           ProposalState = "approved"
	StateDenied             ProposalState = "denied"
	StateExpired            ProposalState = "expired"
	StateRunning            ProposalState = "running"
	StateSucceeded          ProposalState = "succeeded"
	StateFailed             ProposalState = "failed"
	StatePreconditionFailed ProposalState = "precondition_failed"
)

// transitions is the whole state machine. A state absent from this map, or
// a destination not listed under it, is not reachable — that absence IS the
// enforcement of "a script with no working dry run cannot be approved":
// StateDryRunFailed has no edge to StateAwaitingApproval, so Approve can
// never succeed on it no matter what a caller passes.
var transitions = map[ProposalState][]ProposalState{
	StateProposed:         {StateDryRunOK, StateDryRunFailed},
	StateDryRunOK:         {StateAwaitingApproval},
	StateDryRunFailed:     {StateProposed}, // C3: human may edit and retry
	StateAwaitingApproval: {StateApproved, StateDenied, StateExpired, StateProposed},
	StateApproved:         {StateRunning, StateProposed}, // C3: edit after approval re-opens the pipeline
	StateDenied:           {},
	StateExpired:          {},
	StateRunning:          {StateSucceeded, StateFailed, StatePreconditionFailed},
	StateSucceeded:        {},
	// C4: a precondition mismatch fails closed to pending — the grant
	// survives, the run does not.
	StatePreconditionFailed: {StateAwaitingApproval},
	StateFailed:             {},
}

// CanTransitionTo reports whether next is a legal successor of s.
func (s ProposalState) CanTransitionTo(next ProposalState) bool {
	for _, n := range transitions[s] {
		if n == next {
			return true
		}
	}
	return false
}

// Terminal reports a state with no further outgoing edges.
func (s ProposalState) Terminal() bool {
	return len(transitions[s]) == 0
}

// TranscriptEntry is one line of dry-run evidence — a shimmed command
// invocation or a sandbox violation. See FINDINGS.md; this is the
// production analogue of the prototype's transcript file line.
type TranscriptEntry struct {
	Time time.Time `json:"time"`
	PID  int       `json:"pid"`
	Kind string    `json:"kind"` // "CMD" or "VIOLATION"
	Raw  string    `json:"raw"`
}

// Proposal moves through ProposalState. RejectReasons and Warnings are
// carried from the dry run: Reasons are why the predicate rejected it (see
// predicate.go), Warnings are honest-ceiling caveats (e.g. "strace
// unavailable") that never block approval on their own.
type Proposal struct {
	ID            string
	ScriptHash    string
	State         ProposalState
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RejectReasons []string
	Warnings      []string
	Transcript    []TranscriptEntry
	ApprovedBy    string
	ApprovedAt    time.Time
	DeniedBy      string
	DeniedAt      time.Time
	DenyReason    string
}

// AuditEntry is one row of the append-only transition log. Every state
// change goes through registry.transition, which writes exactly one of
// these — there is no path that mutates Proposal.State without one.
type AuditEntry struct {
	ID         string
	ProposalID string
	FromState  ProposalState
	ToState    ProposalState
	Actor      string
	Reason     string
	At         time.Time
}

// GrantScope distinguishes a one-session grant from a standing one
// (coordinator decision C2, GitHub #55).
type GrantScope string

const (
	ScopeSession  GrantScope = "session"
	ScopeStanding GrantScope = "standing"
)

// Grant binds to a Script's content hash, never a name (issue #51). A
// session grant additionally binds to a server-issued SessionID with a hard
// TTL — "this session only" must be mechanically checkable for a stateless
// MCP caller (C2), so the TCP connection itself cannot be the unit of a
// session.
type Grant struct {
	ID         string
	ScriptHash string
	Scope      GrantScope
	SessionID  string // required when Scope == ScopeSession
	APIKeyID   string
	GrantedAt  time.Time
	ExpiresAt  time.Time
	LastRunAt  time.Time // zero value: never run
	RevokedAt  time.Time // zero value: not revoked
}

// Expired reports whether the grant's TTL has elapsed as of now. Expiry is
// checked at use time only (C5) — nothing sweeps expired grants in the
// background, so a grant can sit expired-but-unrevoked in the store
// indefinitely without being usable.
func (g Grant) Expired(now time.Time) bool {
	return !g.ExpiresAt.IsZero() && !now.Before(g.ExpiresAt)
}

// Revoked reports whether a human has instantly revoked the grant.
func (g Grant) Revoked() bool {
	return !g.RevokedAt.IsZero()
}

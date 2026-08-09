package scripts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Store is the persistence seam Registry depends on (CONVENTIONS rule 2).
// The production implementation is store.go's sqlStore, backed by sqlc
// queries against internal/store/migrations/00006_scripts.sql; tests may
// use any implementation.
type Store interface {
	UpsertScript(ctx context.Context, s Script) error
	GetScript(ctx context.Context, hash string) (Script, error)

	InsertProposal(ctx context.Context, p Proposal) error
	UpdateProposal(ctx context.Context, p Proposal) error
	GetProposal(ctx context.Context, id string) (Proposal, error)
	ListPendingProposals(ctx context.Context) ([]Proposal, error)
	FindApprovedProposalByHash(ctx context.Context, hash string) (Proposal, bool, error)

	AppendAudit(ctx context.Context, e AuditEntry) error
	ListAudit(ctx context.Context, proposalID string) ([]AuditEntry, error)

	InsertGrant(ctx context.Context, g Grant) error
	GetGrant(ctx context.Context, id string) (Grant, error)
	ListGrants(ctx context.Context) ([]Grant, error)
	RevokeGrant(ctx context.Context, id string, at time.Time) error
	TouchGrantLastRun(ctx context.Context, id string, at time.Time) error
}

// PreconditionChecker re-validates that an approved script's assumptions
// still hold immediately before a real run (coordinator decision C4,
// GitHub #55: a named container gone, a share renamed). The composition
// root wires the real implementation using core.UnraidSource; this package
// has no opinion on what a precondition is. A nil checker means "none
// configured" and Execute proceeds straight to Run.
type PreconditionChecker interface {
	Check(ctx context.Context, scriptBody string) error
}

// Registry orchestrates the full proposal lifecycle: propose, dry run,
// approve/deny/edit, grant, and execute. Every state change goes through
// transition, which is the only place Proposal.State is ever assigned and
// the only place an AuditEntry is written — no implicit state changes.
type Registry struct {
	store   Store
	exec    *Executor
	precond PreconditionChecker
	now     func() time.Time
	newID   func() string
}

// NewRegistry builds a Registry. precond may be nil.
func NewRegistry(store Store, exec *Executor, precond PreconditionChecker) *Registry {
	return &Registry{
		store:   store,
		exec:    exec,
		precond: precond,
		now:     time.Now,
		newID:   newRandomID,
	}
}

func newRandomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// transition is the single choke point for every Proposal.State change: it
// checks the state machine, persists the new state, and appends exactly one
// AuditEntry. Nothing else in this package assigns p.State directly.
func (r *Registry) transition(ctx context.Context, p *Proposal, next ProposalState, actor, reason string) error {
	if !p.State.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, p.State, next)
	}
	from := p.State
	p.State = next
	p.UpdatedAt = r.now()
	if err := r.store.UpdateProposal(ctx, *p); err != nil {
		return fmt.Errorf("scripts: persist transition %s->%s: %w", from, next, err)
	}
	return r.store.AppendAudit(ctx, AuditEntry{
		ID:         r.newID(),
		ProposalID: p.ID,
		FromState:  from,
		ToState:    next,
		Actor:      actor,
		Reason:     reason,
		At:         r.now(),
	})
}

// runDryRunAndAdvance executes the dry run for p's current script and
// advances p to dry_run_ok/awaiting_approval or dry_run_failed. This is the
// mechanical enforcement of "a script with no working dry run cannot be
// approved": only the dry_run_ok branch ever reaches awaiting_approval.
func (r *Registry) runDryRunAndAdvance(ctx context.Context, p *Proposal, script Script, actor string) error {
	result, err := r.exec.DryRun(ctx, script.Body)
	if err != nil {
		return fmt.Errorf("scripts: dry run: %w", err)
	}
	p.RejectReasons = result.Reasons
	p.Warnings = result.Warnings
	p.Transcript = result.Transcript

	if !result.Approved {
		return r.transition(ctx, p, StateDryRunFailed, actor, "dry run rejected: "+joinReasons(result.Reasons))
	}
	if err := r.transition(ctx, p, StateDryRunOK, actor, "dry run approved-for-review"); err != nil {
		return err
	}
	return r.transition(ctx, p, StateAwaitingApproval, actor, "clean dry run: awaiting human approval")
}

func joinReasons(reasons []string) string {
	if len(reasons) == 0 {
		return "(no reasons recorded)"
	}
	out := reasons[0]
	for _, rr := range reasons[1:] {
		out += "; " + rr
	}
	return out
}

// Propose is the entry point for "the LLM proposes some text": it hashes
// the text, stores the Script if new, creates a Proposal in state
// "proposed", and synchronously runs the dry run through to dry_run_ok
// (-> awaiting_approval) or dry_run_failed.
func (r *Registry) Propose(ctx context.Context, text, actor string) (Proposal, error) {
	script := NewScript(text, r.now())
	if err := r.store.UpsertScript(ctx, script); err != nil {
		return Proposal{}, fmt.Errorf("scripts: store script: %w", err)
	}

	p := Proposal{
		ID:         r.newID(),
		ScriptHash: script.SHA256,
		State:      StateProposed,
		CreatedAt:  r.now(),
		UpdatedAt:  r.now(),
	}
	if err := r.store.InsertProposal(ctx, p); err != nil {
		return Proposal{}, fmt.Errorf("scripts: store proposal: %w", err)
	}
	if err := r.store.AppendAudit(ctx, AuditEntry{
		ID: r.newID(), ProposalID: p.ID, FromState: "", ToState: StateProposed,
		Actor: actor, Reason: "proposed", At: r.now(),
	}); err != nil {
		return Proposal{}, err
	}

	if err := r.runDryRunAndAdvance(ctx, &p, script, actor); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// Edit implements C3: a human (or, before approval, potentially the LLM
// re-proposing) may change a proposal's script text. It re-hashes, records
// a diff summary in the audit trail, and resets the proposal to "proposed"
// for a fresh dry run — from ANY non-terminal state, including "approved".
// This is exactly why editing an approved script invalidates its grant:
// once this returns, no proposal exists in state "approved" for the OLD
// hash, and Grant.ScriptHash still points at that now-unapproved hash.
func (r *Registry) Edit(ctx context.Context, proposalID, newText, actor string) (Proposal, error) {
	p, err := r.store.GetProposal(ctx, proposalID)
	if err != nil {
		return Proposal{}, err
	}
	oldScript, err := r.store.GetScript(ctx, p.ScriptHash)
	if err != nil {
		return Proposal{}, err
	}

	newScript := NewScript(newText, r.now())
	if newScript.SHA256 == oldScript.SHA256 {
		return Proposal{}, errors.New("scripts: edit produced an identical script; nothing to do")
	}
	if err := r.store.UpsertScript(ctx, newScript); err != nil {
		return Proposal{}, fmt.Errorf("scripts: store edited script: %w", err)
	}

	summary := diffSummary(oldScript.Body, newScript.Body)
	p.ScriptHash = newScript.SHA256
	if err := r.transition(ctx, &p, StateProposed, actor, "edited: "+summary); err != nil {
		return Proposal{}, err
	}

	if err := r.runDryRunAndAdvance(ctx, &p, newScript, actor); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// diffSummary is deliberately not a full diff — C3 only requires the LLM be
// told THAT the script changed plus a summary, never silently handed
// different text. Stdlib has no diff algorithm and this ticket does not
// need one; byte/line counts are enough to make an edit unmistakable.
func diffSummary(oldBody, newBody string) string {
	oldLines, newLines := countLines(oldBody), countLines(newBody)
	return fmt.Sprintf("%d -> %d bytes, %d -> %d lines", len(oldBody), len(newBody), oldLines, newLines)
}

func countLines(s string) int {
	n := 1
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}

// Approve moves a proposal from awaiting_approval to approved. It is
// impossible to approve a proposal that never reached awaiting_approval —
// the state machine (transitions in domain.go) has no edge into "approved"
// from anywhere else, so a script whose dry run failed cannot reach this
// state no matter what the caller passes.
func (r *Registry) Approve(ctx context.Context, proposalID, actor string) (Proposal, error) {
	p, err := r.store.GetProposal(ctx, proposalID)
	if err != nil {
		return Proposal{}, err
	}
	if p.State != StateAwaitingApproval {
		return Proposal{}, fmt.Errorf("%w: proposal is %s, not awaiting_approval", ErrDryRunRequired, p.State)
	}
	p.ApprovedBy = actor
	p.ApprovedAt = r.now()
	if err := r.transition(ctx, &p, StateApproved, actor, "approved"); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// Deny moves a proposal from awaiting_approval to denied.
func (r *Registry) Deny(ctx context.Context, proposalID, actor, reason string) (Proposal, error) {
	p, err := r.store.GetProposal(ctx, proposalID)
	if err != nil {
		return Proposal{}, err
	}
	p.DeniedBy = actor
	p.DeniedAt = r.now()
	p.DenyReason = reason
	if err := r.transition(ctx, &p, StateDenied, actor, reason); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// Expire moves an awaiting_approval proposal to expired.
func (r *Registry) Expire(ctx context.Context, proposalID, actor string) (Proposal, error) {
	p, err := r.store.GetProposal(ctx, proposalID)
	if err != nil {
		return Proposal{}, err
	}
	if err := r.transition(ctx, &p, StateExpired, actor, "expired"); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

func (r *Registry) GetProposal(ctx context.Context, id string) (Proposal, error) {
	return r.store.GetProposal(ctx, id)
}

func (r *Registry) ListPending(ctx context.Context) ([]Proposal, error) {
	return r.store.ListPendingProposals(ctx)
}

func (r *Registry) AuditLog(ctx context.Context, proposalID string) ([]AuditEntry, error) {
	return r.store.ListAudit(ctx, proposalID)
}

// Execute runs an approved proposal for real. It re-validates preconditions
// immediately before running (C4) and fails closed to awaiting_approval on
// a mismatch — the grant survives, the run does not, and the caller must
// re-approve to try again.
func (r *Registry) Execute(ctx context.Context, proposalID, actor string) (Proposal, *RunResult, error) {
	p, err := r.store.GetProposal(ctx, proposalID)
	if err != nil {
		return Proposal{}, nil, err
	}
	script, err := r.store.GetScript(ctx, p.ScriptHash)
	if err != nil {
		return Proposal{}, nil, err
	}

	if r.precond != nil {
		if perr := r.precond.Check(ctx, script.Body); perr != nil {
			if err := r.transition(ctx, &p, StateRunning, actor, "starting execution"); err != nil {
				return Proposal{}, nil, err
			}
			if err := r.transition(ctx, &p, StatePreconditionFailed, actor, "precondition check failed: "+perr.Error()); err != nil {
				return Proposal{}, nil, err
			}
			if err := r.transition(ctx, &p, StateAwaitingApproval, actor, "returned to pending: grant survives, run does not"); err != nil {
				return Proposal{}, nil, err
			}
			return p, nil, fmt.Errorf("%w: %v", ErrPreconditionFailed, perr)
		}
	}

	if err := r.transition(ctx, &p, StateRunning, actor, "starting execution"); err != nil {
		return Proposal{}, nil, err
	}

	result, runErr := r.exec.Run(ctx, script.Body)
	if runErr != nil {
		if err := r.transition(ctx, &p, StateFailed, actor, "execution error: "+runErr.Error()); err != nil {
			return Proposal{}, nil, err
		}
		return p, nil, runErr
	}

	next := StateSucceeded
	reason := fmt.Sprintf("exit code %d", result.ExitCode)
	if !result.Succeeded {
		next = StateFailed
	}
	if err := r.transition(ctx, &p, next, actor, reason); err != nil {
		return Proposal{}, nil, err
	}
	return p, result, nil
}

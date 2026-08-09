package mcp

import (
	"context"
	"errors"
	"time"
)

// ProposalRef is the B3 fast-return shape: a mutating tool call never
// blocks on human approval — it returns this immediately. The LLM learns
// the outcome by polling get_proposal(id), never a push.
//
// Shape agreed over hub with ScriptRegistry (HL-SA-18, the script-grant
// ticket that implements this seam for real) so both sides compile against
// the identical interface without a later reconciliation.
type ProposalRef struct {
	ProposalID   string
	DashboardURL string
	State        string
}

// ProposalStatus is what GetProposal returns while polling. Reasons
// carries the B4 "allowed alternatives"/explanation once a proposal moves
// out of pending (e.g. why a dry run failed, why it was denied).
type ProposalStatus struct {
	ProposalID string
	State      string
	Reasons    []string
	UpdatedAt  time.Time
}

// ProposalSink is the seam HL-SA-18 implements with the real script-grant
// model. Propose starts a proposal for a mutating action; GetProposal
// polls its current state. HL-SA-17 deliberately does not invent grant
// semantics — no mutating tool is registered against this seam yet, only
// the read-only get_proposal poll (below), which calls GetProposal.
type ProposalSink interface {
	Propose(ctx context.Context, text string) (ProposalRef, error)
	GetProposal(ctx context.Context, id string) (ProposalStatus, error)
}

// ErrProposalsNotConfigured is what NoopProposalSink returns for every
// call: the grant model isn't wired in yet, so this reports "not
// configured" clearly rather than fabricating a pending proposal.
var ErrProposalsNotConfigured = errors.New("proposal tracking is not configured yet")

// ErrProposalNotFound reports that the id is unknown to a sink that is
// otherwise wired and working. It is deliberately distinct from
// ErrProposalsNotConfigured, which means no grant model is wired in at
// all: conflating them tells the LLM to go ask a human to finish wiring a
// seam that is already finished. The composition root translates
// scripts.ErrProposalNotFound into this, because internal/mcp must not
// import internal/scripts (CONVENTIONS rule 2).
var ErrProposalNotFound = errors.New("proposal not found")

// NoopProposalSink is the default ProposalSink until HL-SA-18's grant
// model is wired into the composition root.
type NoopProposalSink struct{}

func (NoopProposalSink) Propose(context.Context, string) (ProposalRef, error) {
	return ProposalRef{}, ErrProposalsNotConfigured
}

func (NoopProposalSink) GetProposal(context.Context, string) (ProposalStatus, error) {
	return ProposalStatus{}, ErrProposalsNotConfigured
}

var _ ProposalSink = NoopProposalSink{}

// registerProposalTools registers get_proposal, the poll side of B3. The
// sink is NoopProposalSink only when no script registry is wired in; the
// deployed build wires propose_script and this returns real states.
func registerProposalTools(s *Server, sink ProposalSink, dashboardBaseURL string) {
	s.Register(Tool{
		Name:     "get_proposal",
		Category: "proposals",
		Description: "Poll a script proposal by id: returns {proposalId, state, reasons, updatedAt}. " +
			"States, in order: proposed, dry_run_ok | dry_run_failed | precondition_failed, " +
			"awaiting_approval, approved | denied | expired, running, succeeded | failed. " +
			"Nothing has touched the host before `running` — a dry run is sandboxed and " +
			"approval only advances the proposal, it does not execute anything. " +
			"An unknown id reports not_found; a build with no mutating tool wired in reports not_configured.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "proposal id returned by a mutating tool call",
				},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
		Required:    []string{"id"},
		Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true},
		Handler: func(ctx context.Context, args map[string]any, _ bool) (ToolResult, error) {
			p, err := sink.GetProposal(ctx, stringArg(args, "id"))
			switch {
			case errors.Is(err, ErrProposalNotFound):
				return structuredError(
					"not_found", err.Error(),
					"re-check the id returned by the mutating tool call",
				), nil
			case errors.Is(err, ErrProposalsNotConfigured):
				alt := "ask a human to finish wiring the mutating-tool grant model (HL-SA-18)"
				if dashboardBaseURL != "" {
					alt = "check " + dashboardBaseURL + " directly; " + alt
				}
				return structuredError("not_configured", err.Error(), alt), nil
			case err != nil:
				return structuredError(
					"unavailable", err.Error(),
					"retry; if it persists, ask a human to check the server logs",
				), nil
			}
			return renderResult(map[string]any{
				"proposalId": p.ProposalID,
				"state":      p.State,
				"reasons":    p.Reasons,
				"updatedAt":  p.UpdatedAt,
			})
		},
	})
}

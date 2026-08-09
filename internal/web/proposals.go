// proposals.go — the Approval surface for MCP-originated script proposals
// (GitHub #57 decision B3): a mutating MCP tool call fast-returns
// {proposal_id, dashboard_url, state:"awaiting_approval"} and the LLM
// caller learns the outcome by polling a get_proposal read tool while a
// human decides here — no blocking call, no push. Approve/Deny share
// handleAPIDecision's (api.go) exact shape and its persist-before-signal
// guarantee.
//
// ProposalSource is deliberately narrow and defined in this package, not
// core: the real proposal registry that will implement it is a follow-up
// ticket (McpSurface owns proposal creation). This ticket only needs the
// seam to compile against a fake.
package web

import (
	"context"
	"net/http"
	"time"
)

// Proposal is one pending script action awaiting a human decision. Decision
// is "" or "pending" while undecided, "approved"/"denied" once acted on —
// deliberately its own small vocabulary rather than importing
// core.ApprovalDecision, since a script proposal is not a HarnessCycle.
type Proposal struct {
	ID           string
	Title        string
	Script       string
	DryRunOutput string
	RequestedBy  string
	RequestedAt  time.Time
	Decision     string
	DecidedBy    string
	DecidedAt    time.Time
}

// ProposalSource is the seam the dashboard's script-proposal Approval
// surface renders against. Approve/Deny must persist the decision
// synchronously — return only once it is durably recorded — strictly
// before the handler's JSON response is written, exactly like
// HarnessSource (ADR 0009/0023): a polling get_proposal MCP call must never
// observe a decision that could still be lost.
type ProposalSource interface {
	Proposals(ctx context.Context) ([]Proposal, error)
	Proposal(ctx context.Context, id string) (Proposal, error)
	Approve(ctx context.Context, id, who string) error
	Deny(ctx context.Context, id, who string) error
}

type proposalRow struct {
	ID           string
	Title        string
	DryRunOutput string
	RequestedBy  string
	RequestedAt  string
	Decision     string
	Pending      bool
	DecidedBy    string
	DecidedAt    string
}

func proposalRowOf(p Proposal) proposalRow {
	return proposalRow{
		ID:           p.ID,
		Title:        p.Title,
		DryRunOutput: p.DryRunOutput,
		RequestedBy:  p.RequestedBy,
		RequestedAt:  timeOrDash(p.RequestedAt),
		Decision:     p.Decision,
		Pending:      p.Decision == "" || p.Decision == "pending",
		DecidedBy:    p.DecidedBy,
		DecidedAt:    timeOrDash(p.DecidedAt),
	}
}

// proposalRowsOf is best-effort like pendingIncidentsFor (incidents.go): a
// nil ProposalSource or a read error simply yields no section rather than a
// broken page.
func proposalRowsOf(ctx context.Context, ps ProposalSource) []proposalRow {
	if ps == nil {
		return nil
	}
	props, err := ps.Proposals(ctx)
	if err != nil {
		return nil
	}
	rows := make([]proposalRow, 0, len(props))
	for _, p := range props {
		rows = append(rows, proposalRowOf(p))
	}
	return rows
}

// handleAPIProposalDecision implements POST /api/unraid/proposals/{id}/approve
// and .../deny, mirroring handleAPIDecision (api.go) exactly: decide is
// ProposalSource.Approve or ProposalSource.Deny as a method expression, so
// both routes share one handler.
func handleAPIProposalDecision(ps ProposalSource, decide func(ProposalSource, context.Context, string, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()

		id := r.PathValue("id")
		p, err := ps.Proposal(ctx, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if p.Decision != "" && p.Decision != "pending" {
			http.Error(w, "proposal is not pending approval", http.StatusConflict)
			return
		}
		who := r.FormValue("who")
		if who == "" {
			who = "operator"
		}
		if err := decide(ps, ctx, id, who); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		updated, err := ps.Proposal(ctx, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toProposalDTO(updated))
	}
}

type proposalDTO struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	DryRunOutput string  `json:"dry_run_output"`
	Decision     string  `json:"decision"`
	DecidedBy    string  `json:"decided_by"`
	DecidedAt    *string `json:"decided_at"`
}

func toProposalDTO(p Proposal) proposalDTO {
	return proposalDTO{
		ID:           p.ID,
		Title:        p.Title,
		DryRunOutput: p.DryRunOutput,
		Decision:     p.Decision,
		DecidedBy:    p.DecidedBy,
		DecidedAt:    rfc3339Ptr(p.DecidedAt),
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"server-assistant/internal/core"
	"server-assistant/internal/mcp"
	"server-assistant/internal/scripts"
	"server-assistant/internal/web"
)

// proposalBridge adapts one *scripts.Registry to the two narrow seams its
// consumers defined independently: web.ProposalSource (the dashboard's
// Approval surface) and mcp.ProposalSink (the LLM's fast-return propose /
// poll pair, GitHub #57 decision B3).
//
// The adapter lives in the composition root on purpose. internal/web and
// internal/mcp must not import internal/scripts — each owns a seam sized to
// its own need, and the root is the only place that knows one registry
// satisfies both (CONVENTIONS rule 2).
type proposalBridge struct {
	reg          *scripts.Registry
	store        scripts.Store
	dashboardURL string
}

// scriptBody resolves a proposal's content-addressed body. A proposal
// carries only the hash — identity in this package is the hash, never a
// name — so the body is a second lookup.
func (b *proposalBridge) scriptBody(ctx context.Context, hash string) string {
	s, err := b.store.GetScript(ctx, hash)
	if err != nil {
		return ""
	}
	return s.Body
}

// transcriptText renders the dry-run evidence for a human reader. It is
// deliberately plain text: the dashboard shows it verbatim under wording
// that says the script *would* do this, never *will* (FINDINGS.md's
// ceiling — a dry run is evidence, not proof).
func transcriptText(p scripts.Proposal) string {
	var sb strings.Builder
	for _, e := range p.Transcript {
		fmt.Fprintf(&sb, "[%s] %s\n", e.Kind, e.Raw)
	}
	for _, r := range p.RejectReasons {
		fmt.Fprintf(&sb, "REJECTED: %s\n", r)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&sb, "WARNING: %s\n", w)
	}
	if sb.Len() == 0 {
		return "(no dry-run output recorded)"
	}
	return sb.String()
}

func (b *proposalBridge) toWeb(ctx context.Context, p scripts.Proposal) web.Proposal {
	wp := web.Proposal{
		ID:           p.ID,
		Title:        "script " + shortHash(p.ScriptHash),
		Script:       b.scriptBody(ctx, p.ScriptHash),
		DryRunOutput: transcriptText(p),
		RequestedAt:  p.CreatedAt,
		RequestedBy:  "mcp",
		Decision:     decisionOf(p.State),
	}
	switch p.State {
	case scripts.StateApproved:
		wp.DecidedBy, wp.DecidedAt = p.ApprovedBy, p.ApprovedAt
	case scripts.StateDenied:
		wp.DecidedBy, wp.DecidedAt = p.DeniedBy, p.DeniedAt
	}
	return wp
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// decisionOf maps the registry's full lifecycle onto the dashboard's small
// three-word vocabulary. Anything not yet decided reads as pending; the
// richer state still travels to the LLM through GetProposal.
func decisionOf(s scripts.ProposalState) string {
	switch s {
	case scripts.StateApproved:
		return "approved"
	case scripts.StateDenied:
		return "denied"
	default:
		return "pending"
	}
}

// --- web.ProposalSource ---

func (b *proposalBridge) Proposals(ctx context.Context) ([]web.Proposal, error) {
	ps, err := b.reg.ListPending(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.Proposal, 0, len(ps))
	for _, p := range ps {
		out = append(out, b.toWeb(ctx, p))
	}
	return out, nil
}

func (b *proposalBridge) Proposal(ctx context.Context, id string) (web.Proposal, error) {
	p, err := b.reg.GetProposal(ctx, id)
	if err != nil {
		return web.Proposal{}, err
	}
	return b.toWeb(ctx, p), nil
}

// Approve persists the human's decision synchronously before returning, so
// a polling get_proposal can never observe a decision that could still be
// lost (ADR 0009/0023). Registry.Approve is itself synchronous and audited.
func (b *proposalBridge) Approve(ctx context.Context, id, who string) error {
	_, err := b.reg.Approve(ctx, id, who)
	return err
}

func (b *proposalBridge) Deny(ctx context.Context, id, who string) error {
	_, err := b.reg.Deny(ctx, id, who, "denied on dashboard")
	return err
}

// webProposals converts a possibly-nil bridge into a possibly-nil interface.
// Assigning a nil *proposalBridge straight into a web.ProposalSource would
// produce a non-nil interface holding a nil pointer, and the dashboard's
// "no ProposalSource configured" branch would never fire.
func webProposals(b *proposalBridge) web.ProposalSource {
	if b == nil {
		return nil
	}
	return b
}

// --- mcp.ProposalSink ---

func (b *proposalBridge) Propose(ctx context.Context, text string) (mcp.ProposalRef, error) {
	p, err := b.reg.Propose(ctx, text, "mcp")
	if err != nil {
		return mcp.ProposalRef{}, err
	}
	return mcp.ProposalRef{
		ProposalID:   p.ID,
		DashboardURL: b.proposalURL(p.ID),
		State:        string(p.State),
	}, nil
}

func (b *proposalBridge) GetProposal(ctx context.Context, id string) (mcp.ProposalStatus, error) {
	p, err := b.reg.GetProposal(ctx, id)
	if err != nil {
		return mcp.ProposalStatus{}, err
	}
	reasons := append([]string{}, p.RejectReasons...)
	reasons = append(reasons, p.Warnings...)
	return mcp.ProposalStatus{
		ProposalID: p.ID,
		State:      string(p.State),
		Reasons:    reasons,
		UpdatedAt:  p.UpdatedAt,
	}, nil
}

func (b *proposalBridge) proposalURL(id string) string {
	if b.dashboardURL == "" {
		return ""
	}
	return strings.TrimRight(b.dashboardURL, "/") + "/unraid#proposal-" + id
}

var (
	_ web.ProposalSource = (*proposalBridge)(nil)
	_ mcp.ProposalSink   = (*proposalBridge)(nil)
)

// registerScriptTools attaches the mutating half of the script surface.
// internal/mcp deliberately shipped only the read-only get_proposal poll —
// it does not invent grant semantics — so the root registers propose_script
// and check_script once a real registry exists.
//
// Neither tool ever executes anything. propose_script runs the mandatory
// dry run and parks the result awaiting a human; that is the entire safety
// story (issue #51).
func registerScriptTools(s *mcp.Server, b *proposalBridge, exec *scripts.Executor) {
	s.Register(mcp.Tool{
		Name:     "propose_script",
		Category: "scripts",
		Description: "Propose a shell script for a human to review and approve. " +
			"The script is dry-run immediately in a sandbox and parked awaiting a " +
			"human decision on the dashboard; nothing runs until a human approves. " +
			"Returns a proposal id to poll with get_proposal. Scripts take no " +
			"arguments and may never write /boot.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"script": map[string]any{
					"type":        "string",
					"description": "The complete shell script body. No arguments are accepted.",
				},
			},
			"required":             []string{"script"},
			"additionalProperties": false,
		},
		Required: []string{"script"},
		// Not readOnly: it creates durable state. Not destructive: it cannot
		// mutate the Host — approval is a separate human act. The hints are a
		// courtesy to well-behaved clients; the registry enforces regardless.
		Annotations: mcp.Annotations{OpenWorldHint: true},
		Handler: func(ctx context.Context, args map[string]any, _ bool) (mcp.ToolResult, error) {
			text, _ := args["script"].(string)
			if strings.TrimSpace(text) == "" {
				return mcp.ToolResult{}, errors.New("script must not be empty")
			}
			ref, err := b.Propose(ctx, text)
			if err != nil {
				return mcp.ToolResult{}, err
			}
			// json.Marshal, not a hand-built format string: %q on a
			// []string yields ["a" "b"] — space-separated, no commas —
			// which is not JSON at all (check_script below shipped
			// exactly that bug). Keys are camelCase to match every other
			// tool on this surface; propose_script used to be the lone
			// snake_case outlier, so a client that had parsed
			// get_proposal's proposalId found nothing here.
			return jsonToolResult(map[string]any{
				"proposalId":   ref.ProposalID,
				"dashboardUrl": ref.DashboardURL,
				"state":        ref.State,
				"note": "A human must approve this on the dashboard before anything runs. " +
					"Poll get_proposal for the outcome.",
			})
		},
	})

	s.Register(mcp.Tool{
		Name:     "check_script",
		Category: "scripts",
		Description: "Dry-run a candidate script and report whether it would be " +
			"eligible for approval, without creating a proposal. Reports only on the " +
			"text supplied.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"script": map[string]any{"type": "string"},
			},
			"required":             []string{"script"},
			"additionalProperties": false,
		},
		Required:    []string{"script"},
		Annotations: mcp.Annotations{ReadOnlyHint: true, IdempotentHint: true},
		Handler: func(ctx context.Context, args map[string]any, _ bool) (mcp.ToolResult, error) {
			text, _ := args["script"].(string)
			// Check takes no store by construction: it must never reveal
			// whether a matching script already exists or is granted, or it
			// becomes a hash oracle over the grant store (decision C6).
			res, err := scripts.Check(ctx, exec, text)
			if err != nil {
				return mcp.ToolResult{}, err
			}
			return jsonToolResult(map[string]any{
				"valid":    res.Valid,
				"reasons":  res.Reasons,
				"warnings": res.Warnings,
			})
		},
	})
}

// jsonToolResult marshals a tool payload instead of hand-building the JSON
// text. A nil []string is normalised to [] rather than null: the LLM should
// read "no reasons", not "reasons unknown" (CONVENTIONS rule 5 — a gap and
// an empty set are different claims).
func jsonToolResult(payload map[string]any) (mcp.ToolResult, error) {
	for k, v := range payload {
		if s, ok := v.([]string); ok && s == nil {
			payload[k] = []string{}
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return mcp.ToolResult{}, fmt.Errorf("render tool result: %w", err)
	}
	return mcp.ToolResult{Content: string(b)}, nil
}

// unraidPrecondition is the C4 checker: immediately before a real run, any
// container the script names by way of `docker ... <name>` must still exist.
// A mismatch fails closed and returns the proposal to awaiting_approval —
// the grant survives, the run does not.
//
// ponytail: name-substring matching only. It is deliberately coarse and
// errs toward refusing; a real parser is warranted only if false refusals
// show up in practice.
type unraidPrecondition struct {
	src     core.UnraidSource
	timeout time.Duration
}

func (u unraidPrecondition) Check(ctx context.Context, body string) error {
	if !strings.Contains(body, "docker ") {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()
	cs, err := u.src.Containers(ctx)
	if err != nil {
		// Cannot tell is not the same as fine (CONVENTIONS rule 5).
		return fmt.Errorf("precondition: cannot read container state: %w", err)
	}
	for _, c := range cs {
		if strings.Contains(body, c.Name) {
			return nil
		}
	}
	return errors.New("precondition: script references docker but names no container that currently exists")
}

var _ scripts.PreconditionChecker = unraidPrecondition{}

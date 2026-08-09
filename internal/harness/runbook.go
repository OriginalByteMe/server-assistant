package harness

import (
	"fmt"

	"server-assistant/internal/core"
)

// runbook is the deterministic fallback Diagnosis (ADR 0009 fail-closed):
// whenever the Reasoner is unreachable, times out, or returns garbage, the
// harness still proposes a bounded, sensible default — restart the
// triggering subject — rather than silently doing nothing. Fallback marks
// it for the Operator and the dashboard.
func runbook(subject string, trigger core.Status) core.Diagnosis {
	return core.Diagnosis{
		Summary: fmt.Sprintf("Runbook fallback: %s reported %s and the Reasoner was unavailable.", subject, trigger),
		Proposed: core.ProposedAction{
			Kind:      core.ActionRestartContainer,
			Subject:   subject,
			Rationale: "Deterministic runbook: restart the service that triggered this cycle.",
		},
		Usage:    core.Usage{Backend: "runbook"},
		Fallback: true,
	}
}

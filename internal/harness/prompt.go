package harness

import (
	"fmt"
	"sort"
	"strings"

	"server-assistant/internal/core"
)

// buildPrompt renders the deterministic, self-contained Diagnosis prompt
// (ADR 0009/0018): it states the trigger, lists every bounded tool's output
// verbatim, and pins the reply to a closed JSON shape so the Reasoner can
// only select a Service — never a container, host, or command — and one
// catalog Action. services is sorted so identical inputs always render an
// identical prompt.
func buildPrompt(trigger core.Status, subject string, calls []core.ToolCall, services []string) string {
	sorted := append([]string(nil), services...)
	sort.Strings(sorted)

	var b strings.Builder
	fmt.Fprintf(&b, "Service %q is reporting status %s.\n\n", subject, trigger)

	b.WriteString("Diagnostic tool output (read-only, already gathered — you cannot run anything else):\n")
	if len(calls) == 0 {
		b.WriteString("(no tool output available)\n")
	}
	for _, c := range calls {
		if c.Err != "" {
			fmt.Fprintf(&b, "- %s: ERROR: %s\n", c.Tool, c.Err)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", c.Tool, c.Output)
	}

	fmt.Fprintf(&b, "\nKnown services (the only valid values for \"subject\"): %s\n\n", strings.Join(sorted, ", "))
	b.WriteString("Reply with exactly one JSON object and nothing else:\n")
	b.WriteString(`{"action":"restart_container"|"none","subject":"<a service name from the list above>","rationale":"<one line>","summary":"<one short paragraph>"}`)
	b.WriteString("\n\nRules: \"subject\" must be one of the known services listed above and is required only when \"action\" is \"restart_container\". Never name a container, host, file path, or shell command — those are resolved by the harness, not you. If nothing here looks actionable, reply with action \"none\".\n")
	return b.String()
}

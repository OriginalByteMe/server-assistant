package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// maxResultBytes bounds a single tool or resource result's rendered JSON
// (coordinator decision B2 — "a full disk-and-SMART dump could be tens of
// kilobytes" and must not blow an LLM's context budget outright). Summary
// projections stay well under this by construction; detail:true is allowed
// to be larger but is still capped here, with an explicit marker rather
// than a silent cut (B2: "Never silently truncate").
//
// ponytail: one fixed cap for every tool and resource, not per-tool
// tuning — add a per-tool override only if a real tool's honest detail
// payload is routinely capped in practice.
const maxResultBytes = 4096

// render marshals v to indented JSON and truncates it to maxResultBytes
// with a visible marker if it overflows.
func render(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	if len(b) <= maxResultBytes {
		return string(b), nil
	}
	// ToValidUTF8 repairs a byte-slice cut that landed mid multi-byte rune;
	// it is the cheapest correct fix, not a truncation-quality knob.
	cut := strings.ToValidUTF8(string(b[:maxResultBytes]), "")
	return fmt.Sprintf(
		"%s\n...TRUNCATED: response exceeded the %d-byte cap. Narrow the request (e.g. a specific device or container) rather than requesting detail on everything at once.",
		cut, maxResultBytes,
	), nil
}

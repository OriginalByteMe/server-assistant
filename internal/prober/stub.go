// Package prober holds Prober implementations. v1 issue 0001 ships only a stub;
// the HTTP probe arrives in issue 0002.
package prober

import (
	"context"

	"server-assistant/internal/core"
)

// Stub is a no-op Prober. It returns StatusUnknown — never DOWN — honouring
// ADR 0005: absence of a real measurement is "can't tell", not "down".
type Stub struct{}

var _ core.Prober = Stub{}

func (Stub) Name() string { return "stub" }

func (Stub) Probe(_ context.Context) (core.ProbeResult, error) {
	return core.ProbeResult{Status: core.StatusUnknown}, nil
}

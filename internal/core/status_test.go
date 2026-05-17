package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A reachable Service answering within its latency threshold is UP.
func TestDeriveStatus_ReachableUnderThresholdIsUp(t *testing.T) {
	got := DeriveStatus(ProbeResult{Status: StatusUp, Latency: 50 * time.Millisecond}, 200*time.Millisecond)
	require.Equal(t, StatusUp, got)
}

// A reachable Service slower than its latency threshold is DEGRADED, not DOWN
// ("slow" = DEGRADED — CONTEXT.md).
func TestDeriveStatus_ReachableOverThresholdIsDegraded(t *testing.T) {
	got := DeriveStatus(ProbeResult{Status: StatusUp, Latency: 500 * time.Millisecond}, 200*time.Millisecond)
	require.Equal(t, StatusDegraded, got)
}

// CONVENTIONS rule 5 / ADR 0005: when the Probe could not determine health,
// derivation must stay UNKNOWN and never collapse to DOWN — even if a latency
// value happens to be set and exceeds the threshold.
func TestDeriveStatus_CantTellStaysUnknownNeverDown(t *testing.T) {
	got := DeriveStatus(ProbeResult{Status: StatusUnknown, Latency: 999 * time.Millisecond}, 200*time.Millisecond)
	require.Equal(t, StatusUnknown, got)
}

// An unreachable Service (Probe confirms it is not answering) is DOWN — this
// is a legitimate determination, distinct from the UNKNOWN can't-tell case.
func TestDeriveStatus_UnreachableIsDown(t *testing.T) {
	got := DeriveStatus(ProbeResult{Status: StatusDown}, 200*time.Millisecond)
	require.Equal(t, StatusDown, got)
}

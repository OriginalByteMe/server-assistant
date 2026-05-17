package core

import "time"

// DeriveStatus computes a Service's Status from one ProbeResult and that
// Service's latency threshold. A Probe is a raw input — it does not itself
// define health (CONTEXT.md); Status is derived here. Per CONVENTIONS rule 5
// the observer never collapses "can't tell" into "down": a non-UP coarse
// Status is passed through unchanged and is never silently downgraded to DOWN.
func DeriveStatus(r ProbeResult, threshold time.Duration) Status {
	if r.Status == StatusUp && r.Latency > threshold {
		return StatusDegraded
	}
	return r.Status
}

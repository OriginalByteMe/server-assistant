// Package core holds the domain types and the seams (Prober, Store, Notifier)
// that the rest of the application is built around. It depends on nothing
// inside the project.
package core

import "time"

// Status is a Service's or Host's derived health. The zero value is
// StatusUnknown by deliberate design: per ADR 0005, "can't tell" must never
// silently become "down". Code paths default to UNKNOWN, never DOWN.
type Status int

const (
	StatusUnknown  Status = iota // observer cannot determine — not a claim about the subject
	StatusUp                     // doing its job
	StatusDegraded               // reachable but slow or partial
	StatusDown                   // confirmed not doing its job
)

func (s Status) String() string {
	switch s {
	case StatusUp:
		return "UP"
	case StatusDegraded:
		return "DEGRADED"
	case StatusDown:
		return "DOWN"
	default:
		return "UNKNOWN"
	}
}

// ProbeResult is the outcome of a single Probe. Status derivation, debounce,
// and persistence are layered on top of this in later issues.
type ProbeResult struct {
	Status  Status
	Latency time.Duration
	Err     error
}

// Alert is a one-way outbound message to the Operator. Distinct from a future
// (M2) two-way Approval — see CONTEXT.md.
type Alert struct {
	Subject string
	Status  Status
	Message string
}

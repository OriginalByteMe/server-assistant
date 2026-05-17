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

// ServiceView is the dashboard's current projection of one Service: its
// committed Status plus the latest observed latency and check time. It is
// what the SSE stream pushes on each committed change.
type ServiceView struct {
	Name        string
	Status      Status
	Latency     time.Duration
	LastChecked time.Time
}

// ProbeSample is one recorded raw Probe outcome for a Service — the unit of
// history. ADR 0002 makes this the TSDB-ready ingestion point; a dedicated
// time-series backend can attach behind the Store seam later without rework.
type ProbeSample struct {
	Service string
	Status  Status
	Latency time.Duration
	At      time.Time
}

// CommittedStatus is a Service's last debounce-committed Status. It is runtime
// state (not config — CONVENTIONS rule 6): persisting it lets the daemon
// resume across restarts from the last known Status instead of re-deriving
// from UNKNOWN and re-alerting.
type CommittedStatus struct {
	Service   string
	Status    Status
	ChangedAt time.Time
}

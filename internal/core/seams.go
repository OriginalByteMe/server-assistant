package core

import "context"

// The seams (CONVENTIONS rule 2). v1 ships lean implementations; richer
// backends (push agent, TSDB, M2 action harness) attach behind these
// interfaces and must never reshape the core (ADR 0006). These interfaces are
// intentionally minimal for issue 0001 and grow in later issues — growth is
// allowed; reshaping the core is not.

// Prober takes one measurement against a Service or Host.
type Prober interface {
	Name() string
	Probe(ctx context.Context) (ProbeResult, error)
}

// Store persists runtime state and history. It never holds configuration
// (CONVENTIONS rule 6). The interface grows additively per issue (ADR 0006 /
// rule 2): issue 0002 adds committed-Status persistence for restart-safety.
type Store interface {
	Migrate(ctx context.Context) error
	RecordProbe(ctx context.Context, p ProbeSample) error
	LoadProbeSamples(ctx context.Context, service string) ([]ProbeSample, error)
	SaveCommittedStatus(ctx context.Context, cs CommittedStatus) error
	LoadCommittedStatuses(ctx context.Context) ([]CommittedStatus, error)
	Close() error
}

// Notifier delivers a one-way Alert to the Operator.
type Notifier interface {
	Notify(ctx context.Context, a Alert) error
}

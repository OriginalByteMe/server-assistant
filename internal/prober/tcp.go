package prober

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"server-assistant/internal/core"
)

// TCP probes a Service by opening a TCP connection to a host:port. It reports
// a coarse reachability Status (UP when the dial succeeds, DOWN otherwise)
// plus the measured latency; the latency-vs-threshold DEGRADED decision
// belongs to core.DeriveStatus (Probes are raw inputs — CONTEXT.md). It feeds
// the same debounce → commit → dashboard → Alert pipeline as the HTTP probe.
// Stdlib net only, explicit per-Probe timeout via context (CONVENTIONS rules
// 1, 4) — keeps the cgo-free static binary (ADR 0004).
//
// The Host reachability gate (ADR 0005) is the same mechanism pointed at the
// Host instead of a Service; NewReachability is a thin semantic alias so the
// dial/cancel/latency logic lives in exactly one place (the tiebreaker rule:
// least maintenance by one person at 2am).
type TCP struct {
	name    string
	address string
	timeout time.Duration
	dialer  net.Dialer
}

var _ core.Prober = (*TCP)(nil)

// NewTCP returns a TCP Prober for a host:port address. timeout is the
// per-Probe deadline enforced via context (CONVENTIONS rule 4).
func NewTCP(name, address string, timeout time.Duration) *TCP {
	return &TCP{name: name, address: address, timeout: timeout}
}

func (p *TCP) Name() string { return p.name }

// Probe opens one TCP connection bounded by the per-Probe timeout layered on
// ctx. A successful dial is UP; a refused/timed-out dial is DOWN with the
// error recorded. A dial aborted because the parent context was canceled
// (daemon shutdown / SIGTERM) is not a measurement of the Service — reporting
// DOWN would commit a false outage and fire a spurious Alert. The observer
// never collapses "can't tell" into "down" (CONVENTIONS rule 5 / ADR 0005):
// surface it as an error so the monitor skips it.
func (p *TCP) Probe(ctx context.Context) (core.ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	start := time.Now()
	conn, err := p.dialer.DialContext(ctx, "tcp", p.address)
	latency := time.Since(start)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return core.ProbeResult{}, fmt.Errorf("tcp probe %s canceled: %w", p.name, err)
		}
		return core.ProbeResult{Status: core.StatusDown, Latency: latency, Err: err}, nil
	}
	_ = conn.Close()
	return core.ProbeResult{Status: core.StatusUp, Latency: latency}, nil
}

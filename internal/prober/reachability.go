package prober

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"server-assistant/internal/core"
)

// Reachability probes whether the Server Assistant box can reach the Host at
// all, by opening a TCP connection to a host:port. It is the canary ADR 0005
// gates Services on: a committed-DOWN reachability Probe means the observer is
// blind, so the monitor turns its Services UNKNOWN (never DOWN) and fires
// exactly one "Host unreachable" Alert. Stdlib net only, explicit per-Probe
// timeout via context (CONVENTIONS rules 1, 4) — no ICMP/raw sockets (those
// need privilege/cgo); a TCP dial to any open Host port is sufficient and
// keeps the cgo-free static binary (ADR 0004).
type Reachability struct {
	name    string
	address string
	timeout time.Duration
	dialer  net.Dialer
}

var _ core.Prober = (*Reachability)(nil)

// NewReachability returns a Host reachability Prober for a host:port address.
// timeout is the per-Probe deadline enforced via context (CONVENTIONS rule 4).
func NewReachability(name, address string, timeout time.Duration) *Reachability {
	return &Reachability{name: name, address: address, timeout: timeout}
}

func (p *Reachability) Name() string { return p.name }

// Probe opens one TCP connection bounded by the per-Probe timeout layered on
// ctx. A successful dial is reachable (UP); a refused/timed-out dial is
// unreachable (DOWN) with the error recorded. A dial aborted because the
// parent context was canceled (daemon shutdown / SIGTERM) is not a
// measurement of the Host — reporting DOWN would gate every Service to
// UNKNOWN and fire a spurious "Host unreachable" Alert on a clean stop. The
// observer never collapses "can't tell" into "down" (CONVENTIONS rule 5 / ADR
// 0005): surface it as an error so the monitor skips it.
func (p *Reachability) Probe(ctx context.Context) (core.ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	start := time.Now()
	conn, err := p.dialer.DialContext(ctx, "tcp", p.address)
	latency := time.Since(start)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return core.ProbeResult{}, fmt.Errorf("reachability probe %s canceled: %w", p.name, err)
		}
		return core.ProbeResult{Status: core.StatusDown, Latency: latency, Err: err}, nil
	}
	_ = conn.Close()
	return core.ProbeResult{Status: core.StatusUp, Latency: latency}, nil
}

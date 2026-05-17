// Package prober holds Prober implementations. The HTTP(S) probe (issue
// 0002 / ARK-6) is the first real Prober; SSH and TCP probes attach behind
// the same core.Prober seam in later issues.
package prober

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"server-assistant/internal/core"
)

// HTTP probes a Service by issuing a GET to its endpoint. It reports a coarse
// reachability Status (UP when the endpoint answers 2xx, DOWN otherwise) plus
// the measured latency; the latency-vs-threshold DEGRADED decision belongs to
// core.DeriveStatus (Probes are raw inputs — CONTEXT.md).
type HTTP struct {
	name    string
	url     string
	timeout time.Duration
	client  *http.Client
}

var _ core.Prober = (*HTTP)(nil)

// NewHTTP returns an HTTP Prober for url. timeout is the per-Service deadline
// enforced on every Probe via context (CONVENTIONS rule 4).
func NewHTTP(name, url string, timeout time.Duration) *HTTP {
	return &HTTP{
		name:    name,
		url:     url,
		timeout: timeout,
		// Transport-level client timeout is a backstop; the per-Probe
		// context deadline (below) is the authoritative bound.
		client: &http.Client{},
	}
}

func (p *HTTP) Name() string { return p.name }

// Probe issues one GET bounded by the per-Service timeout layered on ctx.
func (p *HTTP) Probe(ctx context.Context) (core.ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return core.ProbeResult{}, fmt.Errorf("build request for %s: %w", p.name, err)
	}

	start := time.Now()
	resp, err := p.client.Do(req)
	latency := time.Since(start)
	if err != nil {
		// A probe aborted because the parent context was canceled (daemon
		// shutdown / SIGTERM) is not a measurement of the Service — it is the
		// observer going away. Reporting DOWN here would let an operator-
		// initiated stop commit a false outage and fire a spurious Alert
		// (notably at debounce_n=1). The observer never collapses "can't tell"
		// into "down" (CONVENTIONS rule 5 / ADR 0005); a canceled probe is not
		// even a sample, so surface it as an error and the monitor skips it.
		// The probe's own deadline yields context.DeadlineExceeded (a real
		// "Service didn't answer" signal) and stays DOWN below.
		if errors.Is(err, context.Canceled) {
			return core.ProbeResult{}, fmt.Errorf("probe %s canceled: %w", p.name, err)
		}
		return core.ProbeResult{Status: core.StatusDown, Latency: latency, Err: err}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return core.ProbeResult{Status: core.StatusUp, Latency: latency}, nil
	}
	return core.ProbeResult{
		Status:  core.StatusDown,
		Latency: latency,
		Err:     fmt.Errorf("%s returned HTTP %d", p.name, resp.StatusCode),
	}, nil
}

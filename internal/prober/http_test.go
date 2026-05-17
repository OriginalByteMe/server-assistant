package prober

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// A Service whose endpoint answers 2xx within the timeout probes UP, with a
// measured latency and no error. The Prober reports a coarse reachability
// Status only — DEGRADED is decided later by core.DeriveStatus.
func TestHTTP_HealthyEndpointIsUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewHTTP("web", srv.URL, time.Second)
	res, err := p.Probe(context.Background())

	require.NoError(t, err)
	require.Equal(t, core.StatusUp, res.Status)
	require.Greater(t, res.Latency, time.Duration(0))
	require.NoError(t, res.Err)
}

// An endpoint slower than the per-Service timeout does not hang the Probe: the
// context deadline is enforced (CONVENTIONS rule 4) and the Service probes
// DOWN with the timeout error recorded. The Probe returns well before the
// handler would have finished.
func TestHTTP_TimeoutIsEnforced(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release // hang until the test releases it
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)

	p := NewHTTP("slow", srv.URL, 50*time.Millisecond)

	done := make(chan struct{})
	var res core.ProbeResult
	go func() {
		res, _ = p.Probe(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Probe did not return — timeout not enforced")
	}

	require.Equal(t, core.StatusDown, res.Status)
	require.Error(t, res.Err)
}

// A probe aborted because the daemon is shutting down (parent context
// canceled, e.g. SIGTERM) is NOT a measurement of the Service — it is the
// observer going away. Surfacing it as DOWN would let an operator-initiated
// stop commit a false outage and fire a spurious Alert (notably at
// debounce_n=1). The observer never collapses "can't tell" into "down"
// (CONVENTIONS rule 5 / ADR 0005); a canceled probe is not even a sample, so
// Probe reports an error (which the monitor skips) and never returns DOWN.
// This is distinct from TestHTTP_TimeoutIsEnforced — the probe's own deadline
// is a real "Service didn't answer" signal and stays DOWN.
func TestHTTP_ParentCancelIsNotDown(t *testing.T) {
	reached := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(reached) // request is now in flight at the handler
		<-release      // hang until the test releases it
	}))
	defer srv.Close()
	defer close(release)

	// Long per-probe timeout so the only thing that ends this probe is the
	// parent cancel, not the probe's own deadline.
	p := NewHTTP("svc", srv.URL, 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var res core.ProbeResult
	var err error
	go func() {
		res, err = p.Probe(ctx)
		close(done)
	}()

	<-reached
	cancel() // simulate graceful shutdown

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Probe did not return after parent cancel")
	}

	require.Error(t, err, "shutdown-canceled probe must surface as an error so the monitor skips record/debounce/notify")
	require.True(t, errors.Is(err, context.Canceled), "error must wrap context.Canceled")
	require.NotEqual(t, core.StatusDown, res.Status, "shutdown must never yield DOWN (rule 5 / ADR 0005)")
}

// An endpoint that answers but with a non-2xx status is DOWN: it is reachable
// but not doing its job (CONTEXT.md — "healthy" = does its job, not merely
// that it responds). Latency is still recorded.
func TestHTTP_Non2xxIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewHTTP("err", srv.URL, time.Second)
	res, err := p.Probe(context.Background())

	require.NoError(t, err)
	require.Equal(t, core.StatusDown, res.Status)
	require.Error(t, res.Err)
	require.Greater(t, res.Latency, time.Duration(0))
}

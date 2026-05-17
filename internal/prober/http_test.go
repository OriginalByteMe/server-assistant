package prober

import (
	"context"
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

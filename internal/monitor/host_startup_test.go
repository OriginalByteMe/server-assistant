package monitor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// blockingHostProber blocks its first Probe until release is closed, then
// reports a fixed result. It lets a test pin the cold-start ordering
// invariant deterministically: while the Host probe is in flight no Service
// must have been probed yet.
type blockingHostProber struct {
	release chan struct{}
	res     core.ProbeResult
	calls   atomic.Int64
}

func (b *blockingHostProber) Name() string { return "unraid" }
func (b *blockingHostProber) Probe(context.Context) (core.ProbeResult, error) {
	if b.calls.Add(1) == 1 {
		<-b.release // hold the gate "undetermined" until the test allows it
	}
	return b.res, nil
}

// Cold-start contract (ADR 0005 rule 5): when a Host is configured, the gate
// MUST be established from a real Host measurement before any Service is
// probed. Otherwise a cold start (no persisted Host Status, gate defaults
// open) races the Service loops and a Service can commit a false DOWN and
// fire per-Service Alerts in the window before the first host probe closes
// the gate. The Service Prober must not be called until the Host has been
// probed.
func TestMonitor_HostGateEstablishedBeforeAnyServiceProbe(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	rec := &recordingNotifier{}
	// A Service that would immediately commit DOWN + Alert if it were ever
	// probed before the gate is established (DebounceN=1, fast poll).
	svcP := &fakeProber{res: core.ProbeResult{Status: core.StatusDown}}
	hostP := &blockingHostProber{
		release: make(chan struct{}),
		res:     core.ProbeResult{Status: core.StatusDown}, // unreachable
	}

	m := New(st, rec, []Service{
		{Name: "web", Prober: svcP, Threshold: time.Second, Poll: 2 * time.Millisecond, DebounceN: 1},
	})
	m.SetHost(Host{Name: "unraid", Prober: hostP, Poll: 2 * time.Millisecond, DebounceN: 1})
	require.NoError(t, m.Resume(ctx)) // no persisted status => gate default

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { m.Run(runCtx); close(done) }()

	// The Host probe is blocked. Give the Service loop ample time to tick
	// many times (2ms poll). If service startup is NOT gated on the Host
	// probe, "web" gets probed here and commits a false DOWN + Alert.
	time.Sleep(80 * time.Millisecond)
	require.Equal(t, int64(0), svcP.calls.Load(),
		"Service must not be probed until the Host gate is established (ADR 0005 rule 5)")
	require.Empty(t, rec.all(), "no Alert may fire before the Host gate is established")

	// Let the Host probe complete: it is unreachable, so the gate closes and
	// exactly one Host Alert fires; the Service stays UNKNOWN, never probed.
	close(hostP.release)
	require.Eventually(t, func() bool {
		a := rec.all()
		return len(a) == 1 && a[0].Subject == "unraid"
	}, 2*time.Second, 5*time.Millisecond, "exactly one Host-unreachable Alert")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	require.Equal(t, int64(0), svcP.calls.Load(), "gated Service never probed")
	v, ok := viewByName(m.Snapshot(), "web")
	require.True(t, ok)
	require.Equal(t, core.StatusUnknown, v.Status, "blind Service is UNKNOWN, never DOWN")
	alerts := rec.all()
	require.Len(t, alerts, 1, "only the single Host Alert, never a per-Service one; got %v", alerts)
}

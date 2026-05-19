package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// Hot-reload applies new per-Service knobs to the running monitor without a
// restart and WITHOUT losing runtime Status (ADR 0006 / rule 6). A Service
// that was committed UP stays UP across the reconfigure; only the knobs
// change. Lowering the latency threshold below the observed latency makes the
// very next probe DEGRADED — proof the new threshold took effect live.
func TestMonitor_ReconfigureAppliesThresholdLivePreservingStatus(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	// Seed committed UP so we can prove it survives the reconfigure.
	require.NoError(t, st.SaveCommittedStatus(ctx, core.CommittedStatus{
		Service: "web", Status: core.StatusUp, ChangedAt: time.Now().UTC(),
	}))

	// 50ms latency, threshold 1s ⇒ UP.
	fp := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: 50 * time.Millisecond}}
	m := New(st, &recordingNotifier{}, []Service{
		{Name: "web", Prober: fp, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	require.NoError(t, m.Resume(ctx))
	m.probeOnce(ctx, m.svcs[0])
	v, _ := viewByName(m.Snapshot(), "web")
	require.Equal(t, core.StatusUp, v.Status)

	// Hot-reload: same Service, threshold now 10ms (< 50ms latency), poll 9s.
	m.Reconfigure([]Service{
		{Name: "web", Prober: fp, Threshold: 10 * time.Millisecond, Poll: 9 * time.Second, DebounceN: 1},
	})

	// Status was not reset by the reconfigure itself.
	v, _ = viewByName(m.Snapshot(), "web")
	require.Equal(t, core.StatusUp, v.Status, "reconfigure must not lose runtime Status")

	// Next probe uses the NEW threshold ⇒ DEGRADED (reachable but slow).
	m.probeOnce(ctx, m.svcs[0])
	v, _ = viewByName(m.Snapshot(), "web")
	require.Equal(t, core.StatusDegraded, v.Status, "new threshold applied live, no restart")
}

// An unknown Service name in a reconfigure (a service added/removed in the
// edited file) is ignored — known Services keep working. v1 boundary: the
// Service *set* changing needs a restart; knob changes are live.
func TestMonitor_ReconfigureIgnoresUnknownServices(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fp := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: time.Millisecond}}
	m := New(st, &recordingNotifier{}, []Service{
		{Name: "web", Prober: fp, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	require.NoError(t, m.Resume(ctx))

	m.Reconfigure([]Service{
		{Name: "web", Prober: fp, Threshold: 250 * time.Millisecond, Poll: time.Hour, DebounceN: 1},
		{Name: "ghost", Prober: fp, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})

	require.Len(t, m.svcs, 1, "no new runtime is spun up on reload (set change needs restart)")
	m.probeOnce(ctx, m.svcs[0])
	v, ok := viewByName(m.Snapshot(), "web")
	require.True(t, ok)
	require.Equal(t, core.StatusUp, v.Status)
}

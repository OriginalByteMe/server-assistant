package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// The monitor enforces the rolling-retention window on each probe: a
// pre-existing sample older than the window is pruned when the Service is
// next probed, while the fresh sample is kept. Storage stays bounded (ADR
// 0002) and History exposes the recent trend for the dashboard sparkline.
func TestMonitor_PrunesOldSamplesAndExposesHistory(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	// An old sample, well outside a 1h window.
	old := time.Now().UTC().Add(-3 * time.Hour)
	require.NoError(t, st.RecordProbe(ctx, core.ProbeSample{
		Service: "web", Status: core.StatusUp, Latency: time.Millisecond, At: old,
	}))

	fp := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: 5 * time.Millisecond}}
	m := New(st, &recordingNotifier{}, []Service{
		{Name: "web", Prober: fp, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	m.SetRetention(time.Hour)
	require.NoError(t, m.Resume(ctx))

	m.probeOnce(ctx, m.svcs[0]) // records a fresh sample, then prunes < now-1h

	samples, err := st.LoadProbeSamples(ctx, "web")
	require.NoError(t, err)
	require.Len(t, samples, 1, "the >3h-old sample is pruned; only the fresh one remains")
	require.True(t, samples[0].At.After(old))

	// History exposes the recent trend (oldest→newest) for the sparkline.
	h := m.History("web")
	require.Len(t, h, 1)
	require.Equal(t, core.StatusUp, h[0].Status)
}

// The Host is a first-class subject: its Probe samples are recorded too, so
// the dashboard can render a Host sparkline (ARK-9 AC: "per Service and Host").
func TestMonitor_HostProbeSamplesAreRecorded(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	hostP := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: 2 * time.Millisecond}}
	m := New(st, &recordingNotifier{}, nil)
	m.SetHost(Host{Name: "unraid", Prober: hostP, Poll: time.Hour, DebounceN: 1})
	m.SetRetention(time.Hour)
	require.NoError(t, m.Resume(ctx))

	m.hostProbeOnce(ctx)

	hist := m.History("unraid")
	require.NotEmpty(t, hist, "Host probe samples must be recorded for the Host sparkline")
	require.Equal(t, core.StatusUp, hist[len(hist)-1].Status)
}

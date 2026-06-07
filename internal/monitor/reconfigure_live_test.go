package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

func TestMonitor_ReconfigureLowersPollAppliesImmediately(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fp := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: time.Millisecond}}
	m := New(st, &recordingNotifier{}, []Service{
		{Name: "web", Prober: fp, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	require.NoError(t, m.Resume(ctx))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		m.Run(runCtx)
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after context cancel")
		}
	}()

	require.Eventually(t, func() bool {
		return fp.calls.Load() >= 1
	}, time.Second, 10*time.Millisecond, "expected immediate probe")
	baseline := fp.calls.Load()

	m.Reconfigure([]Service{
		{Name: "web", Prober: fp, Threshold: time.Second, Poll: 5 * time.Millisecond, DebounceN: 1},
	})

	require.Eventually(t, func() bool {
		return fp.calls.Load() >= baseline+3
	}, 2*time.Second, 10*time.Millisecond, "lowered poll must apply without waiting for the old interval")
}

func TestMonitor_ReconfigureDuringBlindWindowPreservesCommitted(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	rec := &recordingNotifier{}
	svcP := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: time.Millisecond}}
	hostP := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: time.Millisecond}}
	m := New(st, rec, []Service{
		{Name: "web", Prober: svcP, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	m.SetHost(Host{Name: "host", Prober: hostP, Poll: time.Hour, DebounceN: 1})
	require.NoError(t, m.Resume(ctx))

	m.hostProbeOnce(ctx)
	m.probeOnce(ctx, m.svcs[0])

	hostP.res = core.ProbeResult{Status: core.StatusDown, Latency: time.Millisecond}
	m.hostProbeOnce(ctx)

	m.Reconfigure([]Service{
		{Name: "web", Prober: svcP, Threshold: time.Second, Poll: time.Hour, DebounceN: 2},
	})

	hostP.res = core.ProbeResult{Status: core.StatusUp, Latency: time.Millisecond}
	m.hostProbeOnce(ctx)
	m.probeOnce(ctx, m.svcs[0])
	m.probeOnce(ctx, m.svcs[0])

	var webAlerts int
	for _, alert := range rec.all() {
		if alert.Subject == "web" {
			webAlerts++
		}
	}
	require.Equal(t, 1, webAlerts, "only the initial service commit should alert")
}

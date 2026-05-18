package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// flipProber returns its first result until flip() is called, then the second
// — a deterministic "Host goes away then comes back" without timing.
type flipProber struct {
	before, after core.ProbeResult
	flipped       bool
	calls         int
}

func (f *flipProber) Name() string { return "flip" }
func (f *flipProber) Probe(context.Context) (core.ProbeResult, error) {
	f.calls++
	if f.flipped {
		return f.after, nil
	}
	return f.before, nil
}

// Recovery contract (ADR 0005): when the Host becomes reachable again, exactly
// ONE "Host reachable" Alert fires and per-Service Status resumes from its
// frozen committed value. A Service that was healthy the whole time goes
// UNKNOWN→UP with NO Alert — the recovery must not double-alert (the Host
// "reachable" Alert is the only one; no spurious per-Service recovery storm).
func TestMonitor_HostRecoveryResumesWithoutDoubleAlert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	now := time.Now().UTC()
	require.NoError(t, st.SaveCommittedStatus(ctx, core.CommittedStatus{Service: "web", Status: core.StatusUp, ChangedAt: now}))

	rec := &recordingNotifier{}
	// The Service is healthy throughout — it was never the problem; the path
	// to the Host was. A correct gate never turns this into a DOWN/recovery.
	webP := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: time.Millisecond}}
	hostP := &flipProber{
		before: core.ProbeResult{Status: core.StatusDown}, // unreachable
		after:  core.ProbeResult{Status: core.StatusUp},   // reachable again
	}

	m := New(st, rec, []Service{
		{Name: "web", Prober: webP, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	m.SetHost(Host{Name: "unraid", Prober: hostP, Poll: time.Hour, DebounceN: 1})
	require.NoError(t, m.Resume(ctx))

	// Blind window: Host unreachable, Service ticks while gated.
	m.hostProbeOnce(ctx)
	m.probeOnce(ctx, m.svcs[0])
	require.Equal(t, int64(0), webP.calls.Load(), "Service not probed while blind")
	v, _ := viewByName(m.Snapshot(), "web")
	require.Equal(t, core.StatusUnknown, v.Status)

	// Host recovers; Service resumes.
	hostP.flipped = true
	m.hostProbeOnce(ctx)
	m.probeOnce(ctx, m.svcs[0])

	v, _ = viewByName(m.Snapshot(), "web")
	require.Equal(t, core.StatusUp, v.Status, "healthy Service resumes UP after recovery")
	require.Greater(t, webP.calls.Load(), int64(0), "Service probed again once sighted")

	alerts := rec.all()
	require.Len(t, alerts, 2, "exactly one 'unreachable' + one 'reachable'; no per-Service storm; got %v", alerts)
	require.Equal(t, "unraid", alerts[0].Subject)
	require.Contains(t, alerts[0].Message, "unreachable")
	require.Equal(t, "unraid", alerts[1].Subject)
	require.Contains(t, alerts[1].Message, "reachable")
	require.Equal(t, core.StatusUp, alerts[1].Status, "recovery Alert is the Host's, Status UP")
}

// With no Host configured the Monitor is the bare v1 spine: no gate, Services
// derive Status exactly as before (ADR 0006 rule 2 — attach behind the seam,
// never reshape it). A DOWN Service is DOWN, not gated to UNKNOWN.
func TestMonitor_NoHostConfiguredIsUngatedSpine(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	rec := &recordingNotifier{}
	webP := &fakeProber{res: core.ProbeResult{Status: core.StatusDown}}
	m := New(st, rec, []Service{
		{Name: "web", Prober: webP, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	// No SetHost.
	require.NoError(t, m.Resume(ctx))

	m.probeOnce(ctx, m.svcs[0])

	v, ok := viewByName(m.Snapshot(), "web")
	require.True(t, ok)
	require.Equal(t, core.StatusDown, v.Status, "no Host => no gate => a DOWN Service is DOWN")
	require.Greater(t, webP.calls.Load(), int64(0), "Service is probed normally with no gate")
	alerts := rec.all()
	require.Len(t, alerts, 1)
	require.Equal(t, "web", alerts[0].Subject)
}

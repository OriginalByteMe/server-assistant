package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// A reachable-but-DEGRADED Host (ssh_metrics: disk/load/memory pressure, yet
// the box answers and is probeable) must NOT close the ADR 0005 gate. Codex
// P1: hostProbeOnce collapsed every non-UP probe result into unreachable/DOWN,
// so HostMetricsProbe's DEGRADED flipped all Services to UNKNOWN and fired a
// false "Host unreachable" Alert even though the Host was reachable. Rule 5 /
// ADR 0005: degraded is not unreachable — the observer must not lie. The gate
// closes ONLY on a genuinely unreachable (DOWN) Host; DEGRADED keeps the gate
// open, Services derive their real Status, and the Host row shows DEGRADED.
func TestMonitor_DegradedHostKeepsGateOpenNoFalseUnreachableAlert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	rec := &recordingNotifier{}
	// Service is genuinely UP: a wrongly-closed gate would force it UNKNOWN.
	svcP := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: time.Millisecond}}
	// Host metrics: reachable + probeable, but disk/load pressure ⇒ DEGRADED.
	hostP := &fakeProber{res: core.ProbeResult{Status: core.StatusDegraded, Latency: 2 * time.Millisecond}}

	m := New(st, rec, []Service{
		{Name: "web", Prober: svcP, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	m.SetHost(Host{Name: "unraid", Prober: hostP, Poll: time.Hour, DebounceN: 1})
	require.NoError(t, m.Resume(ctx))

	m.hostProbeOnce(ctx)
	m.probeOnce(ctx, m.svcs[0])

	snap := m.Snapshot()

	// Gate stayed OPEN: the Service was actually probed and derives its real
	// Status — it was not frozen to UNKNOWN.
	require.Equal(t, int64(1), svcP.calls.Load(), "reachable host ⇒ Service still probed")
	web, ok := viewByName(snap, "web")
	require.True(t, ok)
	require.Equal(t, core.StatusUp, web.Status, "a degraded Host must not blind its Services")

	// The Host is a first-class subject: its row shows the TRUE Status, not a
	// collapsed DOWN (rule 5 — the observer never lies).
	host, ok := viewByName(snap, "unraid")
	require.True(t, ok)
	require.Equal(t, core.StatusDegraded, host.Status, "degraded is not down")

	// No false "Host unreachable" Alert — the box is reachable.
	for _, a := range rec.all() {
		require.NotContains(t, a.Message, "unreachable",
			"reachable-but-degraded Host must never alert as unreachable")
	}
}

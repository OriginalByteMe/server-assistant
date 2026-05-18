package monitor

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
	"server-assistant/internal/store"
)

// recordingNotifier captures every Alert so a test can assert exactly how many
// fired and what they said — no network (CONVENTIONS rule 9).
type recordingNotifier struct {
	mu     sync.Mutex
	alerts []core.Alert
}

func (n *recordingNotifier) Notify(_ context.Context, a core.Alert) error {
	n.mu.Lock()
	n.alerts = append(n.alerts, a)
	n.mu.Unlock()
	return nil
}

func (n *recordingNotifier) all() []core.Alert {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]core.Alert(nil), n.alerts...)
}

func viewByName(vs []core.ServiceView, name string) (core.ServiceView, bool) {
	for _, v := range vs {
		if v.Name == name {
			return v, true
		}
	}
	return core.ServiceView{}, false
}

// The ADR 0005 gate: when the Host's latest reachability Probe says
// unreachable, every Service under it becomes UNKNOWN — never DOWN — and
// exactly ONE "Host unreachable" Alert fires, never one per Service. While
// blind the Service Probers are not even called: the debouncer is frozen, so
// no false DOWN can commit (CONVENTIONS rule 5).
func TestMonitor_HostUnreachableGatesServicesToUnknownOneAlert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	// Seed both Services committed UP: a buggy gate that let Probes through
	// would commit a real UP→DOWN transition and alert per Service.
	now := time.Now().UTC()
	require.NoError(t, st.SaveCommittedStatus(ctx, core.CommittedStatus{Service: "web", Status: core.StatusUp, ChangedAt: now}))
	require.NoError(t, st.SaveCommittedStatus(ctx, core.CommittedStatus{Service: "api", Status: core.StatusUp, ChangedAt: now}))

	rec := &recordingNotifier{}
	webP := &fakeProber{res: core.ProbeResult{Status: core.StatusDown}}
	apiP := &fakeProber{res: core.ProbeResult{Status: core.StatusDown}}
	hostP := &fakeProber{res: core.ProbeResult{Status: core.StatusDown}} // unreachable

	m := New(st, rec, []Service{
		{Name: "web", Prober: webP, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
		{Name: "api", Prober: apiP, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	m.SetHost(Host{Name: "unraid", Prober: hostP, Poll: time.Hour, DebounceN: 1})
	require.NoError(t, m.Resume(ctx))

	// Host observed unreachable, then the Service loops tick while blind.
	m.hostProbeOnce(ctx)
	m.probeOnce(ctx, m.svcs[0])
	m.probeOnce(ctx, m.svcs[1])

	snap := m.Snapshot()
	web, ok := viewByName(snap, "web")
	require.True(t, ok)
	require.Equal(t, core.StatusUnknown, web.Status, "blind Service is UNKNOWN")
	require.NotEqual(t, core.StatusDown, web.Status, "can't-tell must never be DOWN (ADR 0005)")
	api, ok := viewByName(snap, "api")
	require.True(t, ok)
	require.Equal(t, core.StatusUnknown, api.Status)

	// Debouncer frozen: a gated Service is not probed at all, so no DOWN can
	// ever commit during the blind window.
	require.Equal(t, int64(0), webP.calls.Load(), "gated Service must not be probed")
	require.Equal(t, int64(0), apiP.calls.Load())

	alerts := rec.all()
	require.Len(t, alerts, 1, "exactly one Alert, never one per Service; got %v", alerts)
	require.Equal(t, "unraid", alerts[0].Subject, "the single Alert is the Host's, not a Service's")
	require.Contains(t, alerts[0].Message, "unreachable")

	// The Host is itself a first-class subject on the dashboard.
	host, ok := viewByName(snap, "unraid")
	require.True(t, ok, "Host renders as its own row")
	require.Equal(t, core.StatusDown, host.Status)
}

func openStore(t *testing.T) core.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "g.db"))
	require.NoError(t, err)
	require.NoError(t, st.Migrate(ctx))
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

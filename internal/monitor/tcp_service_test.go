package monitor

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
	"server-assistant/internal/prober"
)

// ARK-8 acceptance: a TCP-probed Service is a first-class citizen of the same
// spine as an HTTP Service — it commits Status through the debouncer, shows
// up on the dashboard, and fires Alerts identically. The pipeline is
// prober-agnostic (core.Prober); this proves a real prober.NewTCP flows
// through it end to end with no special-casing.
func TestMonitor_TCPServiceFlowsThroughPipelineLikeHTTP(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	rec := &recordingNotifier{}

	// An open port: the TCP Service is reachable => UP.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	tcpP := prober.NewTCP("gameserver", ln.Addr().String(), time.Second)
	m := New(st, rec, []Service{
		{Name: "gameserver", Prober: tcpP, Threshold: time.Second, Poll: time.Hour, DebounceN: 1},
	})
	require.NoError(t, m.Resume(ctx))

	m.probeOnce(ctx, m.svcs[0]) // reachable -> commits UP, view + alert

	v, ok := viewByName(m.Snapshot(), "gameserver")
	require.True(t, ok, "TCP Service appears on the dashboard")
	require.Equal(t, core.StatusUp, v.Status)
	alerts := rec.all()
	require.Len(t, alerts, 1, "TCP Service fires an Alert on committed change like HTTP")
	require.Equal(t, "gameserver", alerts[0].Subject)
	require.Equal(t, core.StatusUp, alerts[0].Status)

	// Close the port: same debounce/commit rules => DOWN + one Alert.
	require.NoError(t, ln.Close())
	m.probeOnce(ctx, m.svcs[0])

	v, _ = viewByName(m.Snapshot(), "gameserver")
	require.Equal(t, core.StatusDown, v.Status, "refused TCP => DOWN, same pipeline")
	alerts = rec.all()
	require.Len(t, alerts, 2)
	require.Equal(t, core.StatusDown, alerts[1].Status)
}

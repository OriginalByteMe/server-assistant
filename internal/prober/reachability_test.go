package prober

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// The Host is reachable when a TCP connection to its address succeeds: the
// reachability Probe reports UP with a measured latency and no error. This is
// the canary ADR 0005 hangs the gate on — "can we reach the Host at all".
func TestReachability_OpenPortIsUp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	p := NewReachability("unraid", ln.Addr().String(), time.Second)
	res, err := p.Probe(context.Background())

	require.NoError(t, err)
	require.Equal(t, core.StatusUp, res.Status)
	require.Greater(t, res.Latency, time.Duration(0))
	require.NoError(t, res.Err)
}

// An unreachable Host (nothing listening) is DOWN with the dial error recorded
// — never a panic, never UNKNOWN-as-error. The monitor turns this committed
// DOWN into the single "Host unreachable" gate (ADR 0005), not the caller.
func TestReachability_NoListenerIsDown(t *testing.T) {
	// Bind then immediately close to obtain an address that is guaranteed
	// closed without racing a real port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	p := NewReachability("unraid", addr, time.Second)
	res, err := p.Probe(context.Background())

	require.NoError(t, err)
	require.Equal(t, core.StatusDown, res.Status)
	require.Error(t, res.Err)
}

// A probe aborted because the daemon is shutting down (parent context
// canceled, e.g. SIGTERM) is NOT a measurement of the Host — it is the
// observer going away. Surfacing it as DOWN would gate every Service to
// UNKNOWN and fire a spurious "Host unreachable" Alert on a clean stop. The
// observer never collapses "can't tell" into "down" (CONVENTIONS rule 5 / ADR
// 0005): Probe reports an error (the monitor skips it) and never returns DOWN.
func TestReachability_ParentCancelIsNotDown(t *testing.T) {
	// A listener that never accepts, so the only thing ending the probe is the
	// parent cancel — not a refused connection.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	p := NewReachability("unraid", ln.Addr().String(), 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already shutting down before the dial starts

	res, err := p.Probe(ctx)
	require.Error(t, err, "shutdown-canceled probe must surface as an error so the monitor skips it")
	require.True(t, errors.Is(err, context.Canceled), "error must wrap context.Canceled")
	require.NotEqual(t, core.StatusDown, res.Status, "shutdown must never yield DOWN (rule 5 / ADR 0005)")
}

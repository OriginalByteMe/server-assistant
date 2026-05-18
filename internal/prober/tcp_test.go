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

// A non-HTTP Service whose TCP port accepts a connection probes UP, with a
// measured latency and no error — the coarse reachability Status only;
// DEGRADED is decided later by core.DeriveStatus (Probes are raw inputs).
func TestTCP_OpenPortIsUp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	p := NewTCP("gameserver", ln.Addr().String(), time.Second)
	res, err := p.Probe(context.Background())

	require.NoError(t, err)
	require.Equal(t, core.StatusUp, res.Status)
	require.Greater(t, res.Latency, time.Duration(0))
	require.NoError(t, res.Err)
}

// A refused/unreachable port is DOWN with the dial error recorded — feeding
// the same debounce/commit pipeline as HTTP (CONTEXT.md: a Probe is a raw
// input; Status is derived).
func TestTCP_RefusedPortIsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close()) // now guaranteed closed

	p := NewTCP("db", addr, time.Second)
	res, err := p.Probe(context.Background())

	require.NoError(t, err)
	require.Equal(t, core.StatusDown, res.Status)
	require.Error(t, res.Err)
}

// A probe aborted because the daemon is shutting down (parent context
// canceled) is NOT a measurement of the Service — surfacing DOWN would let an
// operator-initiated stop commit a false outage and fire a spurious Alert.
// The observer never collapses "can't tell" into "down" (rule 5 / ADR 0005).
func TestTCP_ParentCancelIsNotDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	p := NewTCP("svc", ln.Addr().String(), 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already shutting down

	res, err := p.Probe(ctx)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled), "error must wrap context.Canceled")
	require.NotEqual(t, core.StatusDown, res.Status, "shutdown must never yield DOWN (rule 5 / ADR 0005)")
}

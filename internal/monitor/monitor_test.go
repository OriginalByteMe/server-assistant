package monitor

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
	"server-assistant/internal/notifier"
	"server-assistant/internal/store"
)

// fakeProber returns a fixed result and counts calls — no network in unit
// tests (rule 9).
type fakeProber struct {
	calls atomic.Int64
	res   core.ProbeResult
}

func (f *fakeProber) Name() string { return "fake" }
func (f *fakeProber) Probe(context.Context) (core.ProbeResult, error) {
	f.calls.Add(1)
	return f.res, nil
}

// The poll loop probes, persists, and then exits cleanly when its context is
// cancelled — no hang, no leaked goroutine (CONVENTIONS rule 4).
func TestMonitor_RunProbesThenStopsOnCancel(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "m.db"))
	require.NoError(t, err)
	require.NoError(t, st.Migrate(ctx))
	defer func() { require.NoError(t, st.Close()) }()

	fp := &fakeProber{res: core.ProbeResult{Status: core.StatusUp, Latency: 5 * time.Millisecond}}
	m := New(st, notifier.Stub{}, []Service{{
		Name: "web", Prober: fp, Threshold: time.Second, Poll: 10 * time.Millisecond, DebounceN: 2,
	}})

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { m.Run(runCtx); close(done) }()

	// Let a few poll ticks happen, then cancel.
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}

	require.Greater(t, fp.calls.Load(), int64(1), "expected multiple probes")

	// Probes were persisted; after DebounceN agreeing UP, Status committed UP.
	samples, err := st.LoadProbeSamples(ctx, "web")
	require.NoError(t, err)
	require.NotEmpty(t, samples)

	snap := m.Snapshot()
	require.Len(t, snap, 1)
	require.Equal(t, "web", snap[0].Name)
	require.Equal(t, core.StatusUp, snap[0].Status)
}

// Resume seeds committed Status from the Store so a restart does not re-alert
// an already-known Status (CONTEXT.md restart-safety).
func TestMonitor_ResumeSeedsFromStore(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	require.NoError(t, err)
	require.NoError(t, st.Migrate(ctx))
	defer func() { require.NoError(t, st.Close()) }()

	at := time.Date(2026, 5, 17, 9, 0, 0, 0, time.UTC)
	require.NoError(t, st.SaveCommittedStatus(ctx, core.CommittedStatus{
		Service: "web", Status: core.StatusDown, ChangedAt: at,
	}))

	m := New(st, notifier.Stub{}, []Service{{
		Name: "web", Prober: &fakeProber{}, Threshold: time.Second, Poll: time.Hour, DebounceN: 2,
	}})
	require.NoError(t, m.Resume(ctx))

	snap := m.Snapshot()
	require.Len(t, snap, 1)
	require.Equal(t, core.StatusDown, snap[0].Status)
	require.True(t, at.Equal(snap[0].LastChecked))
}

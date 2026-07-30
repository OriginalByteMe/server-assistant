package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
	"server-assistant/internal/store"
)

// A Harness observation is downstream of the v1 commit. If that observation
// fails, the committed Status must still persist, Alert, and remain visible on
// the dashboard; M2 failure degrades to plain monitoring (ADR 0009).
func TestMonitor_CommitSinkFailureCannotAlterCommittedStatus(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:commit-sink-failure?mode=memory&cache=shared")
	require.NoError(t, err)
	require.NoError(t, st.Migrate(ctx))
	defer func() { require.NoError(t, st.Close()) }()

	rec := &recordingNotifier{}
	m := New(st, rec, []Service{{
		Name: "web", Prober: &fakeProber{res: core.ProbeResult{Status: core.StatusDown}},
		Threshold: time.Second, Poll: time.Hour, DebounceN: 1,
	}})

	observed := make(chan core.CommittedStatus, 1)
	m.SetCommitSink(func(_ context.Context, status core.CommittedStatus) error {
		observed <- status
		return errors.New("shadow observation failed")
	})
	require.NoError(t, m.Resume(ctx))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		m.Run(runCtx)
		close(done)
	}()

	select {
	case got := <-observed:
		require.Equal(t, "web", got.Service)
		require.Equal(t, core.StatusDown, got.Status)
	case <-time.After(2 * time.Second):
		t.Fatal("commit sink did not observe committed DOWN")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Monitor.Run did not stop after cancellation")
	}

	statuses, err := st.LoadCommittedStatuses(ctx)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Equal(t, core.StatusDown, statuses[0].Status)

	alerts := rec.all()
	require.Len(t, alerts, 1)
	require.Equal(t, core.StatusDown, alerts[0].Status)

	view, ok := viewByName(m.Snapshot(), "web")
	require.True(t, ok)
	require.Equal(t, core.StatusDown, view.Status)
}

type commitFailingStore struct {
	core.Store
}

func (commitFailingStore) SaveCommittedStatus(context.Context, core.CommittedStatus) error {
	return errors.New("commit unavailable")
}

// A failed persistence attempt is not a durable commit. The v1 Alert and
// dashboard still reflect the in-memory debounce result, but downstream M2
// observation must not run against a Status the Store could not save.
func TestMonitor_CommitSinkRequiresPersistedStatus(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "file:commit-sink-persistence?mode=memory&cache=shared")
	require.NoError(t, err)
	require.NoError(t, st.Migrate(ctx))
	defer func() { require.NoError(t, st.Close()) }()

	rec := &recordingNotifier{}
	m := New(commitFailingStore{Store: st}, rec, []Service{{
		Name: "web", Prober: &fakeProber{res: core.ProbeResult{Status: core.StatusDown}},
		Threshold: time.Second, Poll: time.Hour, DebounceN: 1,
	}})

	observed := make(chan core.CommittedStatus, 1)
	m.SetCommitSink(func(_ context.Context, status core.CommittedStatus) error {
		observed <- status
		return nil
	})
	require.NoError(t, m.Resume(ctx))
	updates, cancelSubscription := m.Subscribe()
	defer cancelSubscription()

	runCtx, cancelRun := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		m.Run(runCtx)
		close(done)
	}()

	select {
	case got := <-updates:
		require.Equal(t, core.StatusDown, got.Status)
	case <-time.After(2 * time.Second):
		t.Fatal("dashboard did not receive in-memory DOWN")
	}
	cancelRun()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Monitor.Run did not stop after cancellation")
	}

	select {
	case got := <-observed:
		t.Fatalf("commit sink observed unpersisted Status: %+v", got)
	default:
	}

	statuses, err := st.LoadCommittedStatuses(ctx)
	require.NoError(t, err)
	require.Empty(t, statuses)

	alerts := rec.all()
	require.Len(t, alerts, 1)
	require.Equal(t, core.StatusDown, alerts[0].Status)

	view, ok := viewByName(m.Snapshot(), "web")
	require.True(t, ok)
	require.Equal(t, core.StatusDown, view.Status)
}

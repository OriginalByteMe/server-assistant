package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// A committed Status persists across a process restart: written, the database
// closed and reopened, it loads back unchanged (AC #4 — restart-safe). The
// daemon resumes from the last committed Status instead of re-alerting.
func TestStore_CommittedStatusSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	at := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	s1, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s1.Migrate(ctx))
	require.NoError(t, s1.SaveCommittedStatus(ctx, core.CommittedStatus{
		Service: "web", Status: core.StatusUp, ChangedAt: at,
	}))
	require.NoError(t, s1.Close())

	s2, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s2.Migrate(ctx))
	defer func() { require.NoError(t, s2.Close()) }()

	got, err := s2.LoadCommittedStatuses(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "web", got[0].Service)
	require.Equal(t, core.StatusUp, got[0].Status)
	require.True(t, at.Equal(got[0].ChangedAt), "ChangedAt: want %s got %s", at, got[0].ChangedAt)
}

// Raw Probe samples are recorded as history and survive a reopen, oldest
// first — the TSDB-ready ingestion point (ADR 0002) and the dashboard's
// latency / last-checked source (AC #4).
func TestStore_ProbeSamplesSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "samples.db")
	t0 := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	s1, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s1.Migrate(ctx))
	require.NoError(t, s1.RecordProbe(ctx, core.ProbeSample{
		Service: "web", Status: core.StatusUp, Latency: 40 * time.Millisecond, At: t0,
	}))
	require.NoError(t, s1.RecordProbe(ctx, core.ProbeSample{
		Service: "web", Status: core.StatusDown, Latency: 0, At: t0.Add(time.Minute),
	}))
	require.NoError(t, s1.Close())

	s2, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s2.Migrate(ctx))
	defer func() { require.NoError(t, s2.Close()) }()

	got, err := s2.LoadProbeSamples(ctx, "web")
	require.NoError(t, err)
	require.Len(t, got, 2)

	require.Equal(t, core.StatusUp, got[0].Status)
	require.Equal(t, 40*time.Millisecond, got[0].Latency)
	require.True(t, t0.Equal(got[0].At))

	require.Equal(t, core.StatusDown, got[1].Status)
	require.True(t, t0.Add(time.Minute).Equal(got[1].At))
}

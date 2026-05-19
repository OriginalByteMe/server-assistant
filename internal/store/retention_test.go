package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// Retention: samples older than the rolling window are pruned so storage
// cannot grow unbounded (ADR 0002). Pruning is per-subject and survives a
// reopen — only samples at/after the cutoff remain.
func TestStore_PruneProbeSamplesDropsOldKeepsRecent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retention.db")
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	s1, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s1.Migrate(ctx))

	// Three "web" samples spanning 2h, one unrelated "api" sample.
	require.NoError(t, s1.RecordProbe(ctx, core.ProbeSample{Service: "web", Status: core.StatusUp, Latency: time.Millisecond, At: now.Add(-2 * time.Hour)}))
	require.NoError(t, s1.RecordProbe(ctx, core.ProbeSample{Service: "web", Status: core.StatusUp, Latency: time.Millisecond, At: now.Add(-90 * time.Minute)}))
	require.NoError(t, s1.RecordProbe(ctx, core.ProbeSample{Service: "web", Status: core.StatusUp, Latency: time.Millisecond, At: now.Add(-10 * time.Minute)}))
	require.NoError(t, s1.RecordProbe(ctx, core.ProbeSample{Service: "api", Status: core.StatusUp, Latency: time.Millisecond, At: now.Add(-2 * time.Hour)}))

	// Keep only the last hour for "web".
	require.NoError(t, s1.PruneProbeSamples(ctx, "web", now.Add(-time.Hour)))
	require.NoError(t, s1.Close())

	s2, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s2.Migrate(ctx))
	defer func() { require.NoError(t, s2.Close()) }()

	web, err := s2.LoadProbeSamples(ctx, "web")
	require.NoError(t, err)
	require.Len(t, web, 1, "only the within-window sample survives, across a reopen")
	require.True(t, now.Add(-10*time.Minute).Equal(web[0].At))

	// Pruning is scoped to the subject — other subjects are untouched.
	api, err := s2.LoadProbeSamples(ctx, "api")
	require.NoError(t, err)
	require.Len(t, api, 1, "pruning web must not touch api history")
}

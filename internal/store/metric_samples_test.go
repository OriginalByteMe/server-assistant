package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Metric samples — numeric and textual, ok and gap — survive a round trip
// through Insert -> range query, oldest first, exactly as recorded (the
// sampler's read/write contract, GitHub #61).
func TestStore_MetricSamplesRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metric_samples.db")
	t0 := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	s1, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s1.Migrate(ctx))

	v1, v2 := 0.0, 3.0
	txt := "STARTED"
	require.NoError(t, s1.RecordMetricSample(ctx, MetricSample{
		Series: "smart.reallocated_sector_ct", Subject: "/dev/sdb", Value: &v1, OK: true, SampledAt: t0,
	}))
	require.NoError(t, s1.RecordMetricSample(ctx, MetricSample{
		Series: "smart.reallocated_sector_ct", Subject: "/dev/sdb", Value: &v2, OK: true, SampledAt: t0.Add(time.Hour),
	}))
	require.NoError(t, s1.RecordMetricSample(ctx, MetricSample{
		Series: "array.state", Subject: "array", TextValue: &txt, OK: true, SampledAt: t0,
	}))
	require.NoError(t, s1.Close())

	s2, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s2.Migrate(ctx))
	defer func() { require.NoError(t, s2.Close()) }()

	got, err := s2.LoadMetricSamples(ctx, "smart.reallocated_sector_ct", "/dev/sdb", t0.Add(-time.Hour), t0.Add(2*time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.True(t, got[0].OK)
	require.NotNil(t, got[0].Value)
	require.Equal(t, 0.0, *got[0].Value)
	require.Nil(t, got[0].TextValue)
	require.True(t, t0.Equal(got[0].SampledAt))
	require.NotNil(t, got[1].Value)
	require.Equal(t, 3.0, *got[1].Value)

	arr, err := s2.LoadMetricSamples(ctx, "array.state", "array", t0.Add(-time.Hour), t0.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, arr, 1)
	require.NotNil(t, arr[0].TextValue)
	require.Equal(t, "STARTED", *arr[0].TextValue)
	require.Nil(t, arr[0].Value)

	latest, err := s2.LatestMetricSample(ctx, "smart.reallocated_sector_ct", "/dev/sdb")
	require.NoError(t, err)
	require.True(t, t0.Add(time.Hour).Equal(latest.SampledAt))
}

// Retention: metric samples older than the cutoff are pruned; recent ones
// and other subjects/series survive, across a reopen (GitHub #61 asks
// whether the existing SQLite retention machinery is reused as-is — it is,
// via the same age-cutoff shape as PruneProbeSamples).
func TestStore_PruneMetricSamplesDropsOldKeepsRecent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metric_retention.db")
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	s1, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s1.Migrate(ctx))

	v := 1.0
	require.NoError(t, s1.RecordMetricSample(ctx, MetricSample{
		Series: "capacity.disk", Subject: "/dev/sdb", Value: &v, OK: true, SampledAt: now.Add(-100 * 24 * time.Hour),
	}))
	require.NoError(t, s1.RecordMetricSample(ctx, MetricSample{
		Series: "capacity.disk", Subject: "/dev/sdb", Value: &v, OK: true, SampledAt: now.Add(-10 * 24 * time.Hour),
	}))
	require.NoError(t, s1.RecordMetricSample(ctx, MetricSample{
		Series: "capacity.share", Subject: "media", Value: &v, OK: true, SampledAt: now.Add(-100 * 24 * time.Hour),
	}))

	// Keep only the last 90 days.
	require.NoError(t, s1.PruneMetricSamples(ctx, now.Add(-90*24*time.Hour)))
	require.NoError(t, s1.Close())

	s2, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s2.Migrate(ctx))
	defer func() { require.NoError(t, s2.Close()) }()

	disk, err := s2.LoadMetricSamples(ctx, "capacity.disk", "/dev/sdb", now.Add(-365*24*time.Hour), now)
	require.NoError(t, err)
	require.Len(t, disk, 1, "only the within-window sample survives, across a reopen")
	require.True(t, now.Add(-10*24*time.Hour).Equal(disk[0].SampledAt))

	share, err := s2.LoadMetricSamples(ctx, "capacity.share", "media", now.Add(-365*24*time.Hour), now)
	require.NoError(t, err)
	require.Empty(t, share, "the old share sample is pruned too — prune is global, not per-subject")
}

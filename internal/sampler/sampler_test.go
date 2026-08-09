package sampler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
	"server-assistant/internal/store"
)

// fakeSource is a minimal core.UnraidSource: one array with one disk, one
// share, and a caller-controlled SmartFor outcome. It counts SmartFor calls
// so a test can prove the sampler never retries or forces a read on a
// standby disk.
type fakeSource struct {
	array    core.ArrayState
	shares   []core.Share
	smart    core.SmartAttrs
	smartErr error

	smartCalls int
}

func (f *fakeSource) HostInfo(context.Context) (core.HostInfo, error) { return core.HostInfo{}, nil }
func (f *fakeSource) Array(context.Context) (core.ArrayState, error)  { return f.array, nil }
func (f *fakeSource) Shares(context.Context) ([]core.Share, error)    { return f.shares, nil }
func (f *fakeSource) Containers(context.Context) ([]core.Container, error) {
	return nil, nil
}
func (f *fakeSource) SmartFor(_ context.Context, _ string) (core.SmartAttrs, error) {
	f.smartCalls++
	if f.smartErr != nil {
		return core.SmartAttrs{}, f.smartErr
	}
	return f.smart, nil
}
func (f *fakeSource) Reachability(context.Context) (core.Reachability, error) {
	return core.Reachability{}, nil
}

var _ core.UnraidSource = (*fakeSource)(nil)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "sampler.db"))
	require.NoError(t, err)
	require.NoError(t, s.Migrate(ctx))
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

// A standby disk is skipped, not woken: SmartFor is called exactly once per
// cycle (no retry, no forced read), and the sampler records an explicit gap
// row per tracked SMART series instead of silently producing nothing
// (GitHub #61's governing constraint).
func TestSampler_StandbyDiskSkippedNoWake(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := &fakeSource{
		array: core.ArrayState{
			State: "STARTED",
			Disks: []core.Disk{{Name: "disk1", Device: "/dev/sdd", Role: "data", SpunDown: true}},
		},
		smartErr: core.ErrDiskStandby,
	}
	s := New(src, st, time.Hour, 90*24*time.Hour, nil)

	s.sampleOnce(ctx)

	require.Equal(t, 1, src.smartCalls, "SmartFor must be called exactly once — no retry, no wake attempt")

	points, err := s.Trend(ctx, SeriesSMARTReallocatedSectors, "/dev/sdd", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, points, 1, "the standby skip must produce a recorded gap, not silence")
	require.False(t, points[0].OK)
	require.Nil(t, points[0].Value)
	require.Contains(t, points[0].Note, "standby")

	// A second cycle still does not retry the same disk more than once per
	// cycle, and still does not wake it.
	s.sampleOnce(ctx)
	require.Equal(t, 2, src.smartCalls, "one SmartFor call per cycle, still never forced/retried within a cycle")
}

// A trend query renders a standby gap as an explicit gap between two real
// readings, never interpolating a value across it (CONVENTIONS rule 5).
func TestSampler_TrendRendersGapNotInterpolated(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	t0 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	v0, v2 := 0.0, 20.0
	require.NoError(t, st.RecordMetricSample(ctx, store.MetricSample{
		Series: SeriesSMARTReallocatedSectors, Subject: "/dev/sdb", Value: &v0, OK: true, SampledAt: t0,
	}))
	require.NoError(t, st.RecordMetricSample(ctx, store.MetricSample{
		Series: SeriesSMARTReallocatedSectors, Subject: "/dev/sdb", OK: false, Note: "disk in standby; not woken", SampledAt: t0.Add(time.Hour),
	}))
	require.NoError(t, st.RecordMetricSample(ctx, store.MetricSample{
		Series: SeriesSMARTReallocatedSectors, Subject: "/dev/sdb", Value: &v2, OK: true, SampledAt: t0.Add(2 * time.Hour),
	}))

	s := New(&fakeSource{}, st, time.Hour, 90*24*time.Hour, nil)
	points, err := s.Trend(ctx, SeriesSMARTReallocatedSectors, "/dev/sdb", t0.Add(-time.Minute), t0.Add(3*time.Hour))
	require.NoError(t, err)
	require.Len(t, points, 3)

	require.True(t, points[0].OK)
	require.Equal(t, 0.0, *points[0].Value)

	require.False(t, points[1].OK, "the middle point is the gap — it must stay a gap")
	require.Nil(t, points[1].Value, "a gap must never carry an interpolated value")

	require.True(t, points[2].OK)
	require.Equal(t, 20.0, *points[2].Value)
}

// Array state is recorded only on transition, not every cycle: two
// consecutive cycles with an unchanged state produce one row, not two.
func TestSampler_ArrayStateOnlyRecordsOnTransition(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	src := &fakeSource{array: core.ArrayState{State: "STARTED"}}
	s := New(src, st, time.Hour, 90*24*time.Hour, nil)

	s.sampleOnce(ctx)
	s.sampleOnce(ctx)

	points, err := s.Trend(ctx, SeriesArrayState, "array", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, points, 1, "an unchanged array state must not grow the history")

	src.array.State = "STOPPED"
	s.sampleOnce(ctx)

	points, err = s.Trend(ctx, SeriesArrayState, "array", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, points, 2, "a real transition must record a new row")
	require.Equal(t, "STOPPED", *points[1].Text)
}

// Package sampler is the SMART/capacity/array-state history sampler
// (GitHub #61). It samples the series where history is the signal —
// "reallocated sectors: 8" means nothing alone and a great deal next to
// "was 0 last week" — and deliberately does not sample container state,
// shares or permissions, which are read on demand elsewhere.
//
// The governing constraint is drive spin-up, not resolution (CONVENTIONS
// rule 5, core.ErrDiskStandby): a disk in standby is skipped and the gap is
// recorded explicitly, never woken to fill it in.
package sampler

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"server-assistant/internal/core"
	"server-assistant/internal/store"
)

// The fixed series vocabulary this sampler writes and Trend reads. Series
// names are dotted (`<domain>.<metric>`) so a trend consumer can group by
// domain. This is the sampler's own naming — core and store carry none of
// it.
const (
	SeriesSMARTReallocatedSectors   = "smart.reallocated_sector_ct"
	SeriesSMARTPendingSectors       = "smart.current_pending_sector"
	SeriesSMARTOfflineUncorrectable = "smart.offline_uncorrectable"
	SeriesSMARTTemperature          = "smart.temperature_celsius"
	SeriesSMARTPowerOnHours         = "smart.power_on_hours"
	SeriesCapacityDisk              = "capacity.disk"
	SeriesCapacityShare             = "capacity.share"
	SeriesArrayState                = "array.state"

	// arraySubject is the fixed Subject for the array.state series — there
	// is only ever one array.
	arraySubject = "array"
)

// smartAttrSeries maps the standard ATA S.M.A.R.T. attribute IDs (baseline
// set agreed on GitHub #61: reallocated, pending, offline uncorrectable,
// temperature, power-on hours) to the series this sampler keeps history
// for. Matched by numeric ID rather than name: these five IDs are part of
// the ATA spec itself and are reported identically across vendors, unlike
// many vendor-specific attribute names.
var smartAttrSeries = map[int]string{
	5:   SeriesSMARTReallocatedSectors,
	9:   SeriesSMARTPowerOnHours,
	194: SeriesSMARTTemperature,
	197: SeriesSMARTPendingSectors,
	198: SeriesSMARTOfflineUncorrectable,
}

// Point is one entry in a Trend series — the read-side shape a future MCP
// tool adapts (McpSurface owns registration; this method signature is what
// it adapts). OK false is an explicit gap: Value is nil and must render as
// a gap, never be interpolated from its neighbours (CONVENTIONS rule 5).
type Point struct {
	At    time.Time
	Value *float64
	Text  *string
	OK    bool
	Note  string
}

// Sampler periodically reads SMART, capacity and array state through a
// core.UnraidSource and records history to a *store.Store. Safe for a
// single Run goroutine; Trend is safe to call concurrently with Run.
type Sampler struct {
	source    core.UnraidSource
	store     *store.Store
	interval  time.Duration
	retention time.Duration
	logger    *slog.Logger
}

// New builds a Sampler. interval and retention are the resolved
// config.SamplerConfig values (Interval(), Retention()) — this package
// takes plain durations rather than importing internal/config, so the
// composition root (cmd/server-assistant/main.go, not touched by this
// slice) is the only place that wires config to behaviour.
func New(source core.UnraidSource, st *store.Store, interval, retention time.Duration, logger *slog.Logger) *Sampler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sampler{source: source, store: st, interval: interval, retention: retention, logger: logger}
}

// Run samples immediately, then on every tick until ctx is cancelled
// (CONVENTIONS rule 4 — context-cancellable, stdlib time.Ticker, no cron
// dependency). Never returns an error: a single failed read is logged and
// skipped, not fatal to the loop (rule 10 — daemons don't panic, and one
// bad cycle must not stop the next).
func (s *Sampler) Run(ctx context.Context) {
	s.sampleOnce(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleOnce(ctx)
		}
	}
}

// sampleOnce runs one full sampling cycle — SMART per disk, capacity per
// disk and share, an array-state transition check — followed by a retention
// prune. Each step is independent: one disk's SMART read failing does not
// stop capacity or array sampling.
func (s *Sampler) sampleOnce(ctx context.Context) {
	now := time.Now().UTC()

	arr, err := s.source.Array(ctx)
	if err != nil {
		s.logger.Error("sampler: read array state", "error", err)
	} else {
		s.sampleArrayState(ctx, arr, now)
		for _, d := range arr.Disks {
			s.sampleDiskCapacity(ctx, d, now)
			s.sampleSMART(ctx, d, now)
		}
	}

	shares, err := s.source.Shares(ctx)
	if err != nil {
		s.logger.Error("sampler: read shares", "error", err)
	} else {
		for _, sh := range shares {
			s.recordCapacity(ctx, SeriesCapacityShare, sh.Name, sh.UsedBytes, now)
		}
	}

	cutoff := now.Add(-s.retention)
	if err := s.store.PruneMetricSamples(ctx, cutoff); err != nil {
		s.logger.Error("sampler: prune metric samples", "error", err, "cutoff", cutoff)
	}
}

// sampleArrayState records an array.state row only when the state actually
// changed since the last stored value — this is a transition log, not a
// periodic snapshot (GitHub #61: array/parity state transitions).
func (s *Sampler) sampleArrayState(ctx context.Context, arr core.ArrayState, now time.Time) {
	last, err := s.store.LatestMetricSample(ctx, SeriesArrayState, arraySubject)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logger.Error("sampler: read last array state", "error", err)
		return
	}
	if err == nil && last.TextValue != nil && *last.TextValue == arr.State {
		return // unchanged — no new row
	}
	state := arr.State
	if err := s.store.RecordMetricSample(ctx, store.MetricSample{
		Series: SeriesArrayState, Subject: arraySubject,
		TextValue: &state, OK: true, SampledAt: now,
	}); err != nil {
		s.logger.Error("sampler: record array state", "error", err)
	}
}

func (s *Sampler) sampleDiskCapacity(ctx context.Context, d core.Disk, now time.Time) {
	s.recordCapacity(ctx, SeriesCapacityDisk, d.Name, d.UsedBytes, now)
}

func (s *Sampler) recordCapacity(ctx context.Context, series, subject string, usedBytes int64, now time.Time) {
	v := float64(usedBytes)
	if err := s.store.RecordMetricSample(ctx, store.MetricSample{
		Series: series, Subject: subject, Value: &v, OK: true, SampledAt: now,
	}); err != nil {
		s.logger.Error("sampler: record capacity", "series", series, "subject", subject, "error", err)
	}
}

// sampleSMART reads one disk's raw SMART attributes and records the
// baseline set (smartAttrSeries). On core.ErrDiskStandby it records one
// explicit gap row per tracked attribute and returns — SmartFor is called
// exactly once; there is no retry, no forced read, no wake (GitHub #61's
// governing constraint).
func (s *Sampler) sampleSMART(ctx context.Context, d core.Disk, now time.Time) {
	attrs, err := s.source.SmartFor(ctx, d.Device)
	if err != nil {
		note := err.Error()
		if !errors.Is(err, core.ErrDiskStandby) {
			s.logger.Error("sampler: read smart attrs", "device", d.Device, "error", err)
		}
		for _, series := range smartAttrSeries {
			if rerr := s.store.RecordMetricSample(ctx, store.MetricSample{
				Series: series, Subject: d.Device, OK: false, Note: note, SampledAt: now,
			}); rerr != nil {
				s.logger.Error("sampler: record smart gap", "device", d.Device, "series", series, "error", rerr)
			}
		}
		return
	}
	for _, a := range attrs.Attributes {
		series, tracked := smartAttrSeries[a.ID]
		if !tracked {
			continue
		}
		v := float64(a.RawValue)
		if err := s.store.RecordMetricSample(ctx, store.MetricSample{
			Series: series, Subject: d.Device, Value: &v, OK: true, SampledAt: now,
		}); err != nil {
			s.logger.Error("sampler: record smart attr", "device", d.Device, "series", series, "error", err)
		}
	}
}

// Trend returns a series' samples over [from, to], oldest first, for one
// subject (a disk device, a share name, or "array"). Gap rows (a standby
// skip) come back with OK false and a nil Value/Text — explicit, never
// interpolated (CONVENTIONS rule 5). This is the method McpSurface adapts
// into an MCP trend tool; it registers no tool itself.
func (s *Sampler) Trend(ctx context.Context, series, subject string, from, to time.Time) ([]Point, error) {
	samples, err := s.store.LoadMetricSamples(ctx, series, subject, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]Point, 0, len(samples))
	for _, m := range samples {
		out = append(out, Point{At: m.SampledAt, Value: m.Value, Text: m.TextValue, OK: m.OK, Note: m.Note})
	}
	return out, nil
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"server-assistant/internal/store/db"
)

// MetricSample is one row of sampler history — the series where history is
// the signal (GitHub #61): SMART attributes, per-disk/per-share capacity,
// and array/parity state transitions. Series and Subject together name the
// time series (internal/sampler owns the vocabulary); Value carries a
// numeric series' payload, TextValue a textual series' (e.g. array state).
//
// OK false means the read was deliberately skipped — a disk in standby,
// GitHub #61 — not a failed measurement: Value and TextValue are both nil
// and Note explains why. CONVENTIONS rule 5 (the observer never lies): a
// gap is recorded explicitly, never omitted, never a fake zero, and a trend
// reader must never interpolate across it.
type MetricSample struct {
	Series    string
	Subject   string
	Value     *float64
	TextValue *string
	OK        bool
	Note      string
	SampledAt time.Time
}

// RecordMetricSample appends one sampler reading (or gap) to the history.
func (s *Store) RecordMetricSample(ctx context.Context, m MetricSample) error {
	if err := s.q.InsertMetricSample(ctx, db.InsertMetricSampleParams{
		Series:    m.Series,
		Subject:   m.Subject,
		Value:     nullFloat64(m.Value),
		TextValue: nullString(m.TextValue),
		Ok:        boolToInt64(m.OK),
		Note:      m.Note,
		SampledAt: m.SampledAt.UnixMilli(),
	}); err != nil {
		return fmt.Errorf("record metric sample %s/%s: %w", m.Series, m.Subject, err)
	}
	return nil
}

// LoadMetricSamples returns one series/subject's samples within [from, to],
// oldest first, including gap rows (OK false) exactly as recorded — never
// interpolated, never dropped.
func (s *Store) LoadMetricSamples(ctx context.Context, series, subject string, from, to time.Time) ([]MetricSample, error) {
	rows, err := s.q.ListMetricSamples(ctx, db.ListMetricSamplesParams{
		Series:      series,
		Subject:     subject,
		SampledAt:   from.UnixMilli(),
		SampledAt_2: to.UnixMilli(),
	})
	if err != nil {
		return nil, fmt.Errorf("load metric samples %s/%s: %w", series, subject, err)
	}
	out := make([]MetricSample, 0, len(rows))
	for _, r := range rows {
		out = append(out, MetricSample{
			Series:    r.Series,
			Subject:   r.Subject,
			Value:     floatPtr(r.Value),
			TextValue: stringPtr(r.TextValue),
			OK:        r.Ok != 0,
			Note:      r.Note,
			SampledAt: time.UnixMilli(r.SampledAt).UTC(),
		})
	}
	return out, nil
}

// LatestMetricSample returns the most recent sample for a series/subject.
// Returns sql.ErrNoRows (wrapped) when none exists yet.
func (s *Store) LatestMetricSample(ctx context.Context, series, subject string) (MetricSample, error) {
	r, err := s.q.LatestMetricSample(ctx, db.LatestMetricSampleParams{Series: series, Subject: subject})
	if err != nil {
		return MetricSample{}, fmt.Errorf("latest metric sample %s/%s: %w", series, subject, err)
	}
	return MetricSample{
		Series:    r.Series,
		Subject:   r.Subject,
		Value:     floatPtr(r.Value),
		TextValue: stringPtr(r.TextValue),
		OK:        r.Ok != 0,
		Note:      r.Note,
		SampledAt: time.UnixMilli(r.SampledAt).UTC(),
	}, nil
}

// SeriesSubject is one distinct (series, subject) pair actually present in
// metric_samples — the read-side shape a trend consumer uses to discover
// what it can ask Trend/LoadMetricSamples for, without guessing a series
// name (internal/sampler owns the vocabulary; this is just what has
// actually been recorded).
type SeriesSubject struct {
	Series  string
	Subject string
}

// ListMetricSeries returns every distinct (series, subject) pair with at
// least one recorded sample (gap rows included — a standby-skipped disk is
// still a subject worth discovering). Small, unindexed scan: metric_samples
// is retention-bounded (GitHub #61) and this list is not called per-tick.
func (s *Store) ListMetricSeries(ctx context.Context) ([]SeriesSubject, error) {
	rows, err := s.q.ListMetricSeries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list metric series: %w", err)
	}
	out := make([]SeriesSubject, 0, len(rows))
	for _, r := range rows {
		out = append(out, SeriesSubject{Series: r.Series, Subject: r.Subject})
	}
	return out, nil
}

// PruneMetricSamples deletes every metric sample older than before,
// enforcing the sampler's retention window (GitHub #61). Global rather than
// per-subject: unlike probe_samples, every series shares one retention
// policy, so there is nothing a per-subject scope would buy.
func (s *Store) PruneMetricSamples(ctx context.Context, before time.Time) error {
	if err := s.q.PruneMetricSamples(ctx, before.UnixMilli()); err != nil {
		return fmt.Errorf("prune metric samples: %w", err)
	}
	return nil
}

func nullFloat64(v *float64) sql.NullFloat64 {
	if v == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *v, Valid: true}
}

func nullString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

func stringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

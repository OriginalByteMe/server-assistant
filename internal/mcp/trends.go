package mcp

import (
	"context"
	"time"
)

// TrendPoint is one sample (or explicit gap) in a trend series — the shape
// TrendSource.Trend returns. OK false is a deliberate gap (a standby-skipped
// disk, GitHub #61): Value and Text are both nil and must render as a gap,
// never be interpolated from neighbours (CONVENTIONS rule 5).
//
// This is mcp's own copy of internal/sampler.Point's shape rather than that
// type itself: mcp must not import internal/sampler (the dependency points
// the other way — internal/sampler/mcp_adapter.go imports mcp to satisfy
// TrendSource, not the reverse), so the two packages carry structurally
// identical but distinct types.
type TrendPoint struct {
	At    time.Time
	Value *float64
	Text  *string
	OK    bool
	Note  string
}

// TrendSeriesInfo is one (series, subject) pair actually present in the
// sampler's store — what list_trend_series returns so an LLM can discover
// valid get_smart_trend arguments instead of guessing a dotted series name.
type TrendSeriesInfo struct {
	Series  string
	Subject string
}

// TrendSource is the narrow read seam the trends tools need from the
// sampler (HL-SA-19, GitHub #61's closing question: "what the LLM can ask
// about trends"). Defined here rather than imported from internal/sampler
// so this package's only dependency stays inward, toward core — the
// sampler package (or its composition-root wiring) is what adapts itself
// to this shape, not the other way around.
type TrendSource interface {
	// Trend returns one series/subject's samples over [from, to], oldest
	// first, gap rows included exactly as recorded.
	Trend(ctx context.Context, series, subject string, from, to time.Time) ([]TrendPoint, error)
	// ListTrendSeries returns every (series, subject) pair with at least
	// one recorded sample.
	ListTrendSeries(ctx context.Context) ([]TrendSeriesInfo, error)
}

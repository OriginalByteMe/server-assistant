package sampler

import (
	"context"
	"time"

	"server-assistant/internal/mcp"
)

// TrendSourceAdapter satisfies mcp.TrendSource (HL-SA-19) by delegating to
// a *Sampler's Trend method and the store's series index, converting this
// package's Point into mcp.TrendPoint. This is the only file in the
// repository that imports internal/mcp from internal/sampler — the
// dependency runs from here outward to the interface mcp already
// publishes, never the other way (internal/mcp never imports
// internal/sampler).
type TrendSourceAdapter struct{ s *Sampler }

// NewTrendSourceAdapter wraps s for mcp.ServerOptions.TrendSource.
func NewTrendSourceAdapter(s *Sampler) TrendSourceAdapter { return TrendSourceAdapter{s: s} }

// Trend adapts (*Sampler).Trend's []Point into []mcp.TrendPoint.
func (a TrendSourceAdapter) Trend(ctx context.Context, series, subject string, from, to time.Time) ([]mcp.TrendPoint, error) {
	pts, err := a.s.Trend(ctx, series, subject, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.TrendPoint, len(pts))
	for i, p := range pts {
		out[i] = mcp.TrendPoint{At: p.At, Value: p.Value, Text: p.Text, OK: p.OK, Note: p.Note}
	}
	return out, nil
}

// ListTrendSeries adapts the store's distinct (series, subject) index into
// []mcp.TrendSeriesInfo — list_trend_series' discoverability seam so an
// LLM never has to guess a dotted series name.
func (a TrendSourceAdapter) ListTrendSeries(ctx context.Context) ([]mcp.TrendSeriesInfo, error) {
	pairs, err := a.s.store.ListMetricSeries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.TrendSeriesInfo, len(pairs))
	for i, p := range pairs {
		out[i] = mcp.TrendSeriesInfo{Series: p.Series, Subject: p.Subject}
	}
	return out, nil
}

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTrendSource is a minimal TrendSource double for these tests.
type fakeTrendSource struct {
	series    []TrendSeriesInfo
	seriesErr error
	points    map[string][]TrendPoint // key: series + "|" + subject
	trendErr  error
}

func (f *fakeTrendSource) ListTrendSeries(context.Context) ([]TrendSeriesInfo, error) {
	return f.series, f.seriesErr
}

func (f *fakeTrendSource) Trend(_ context.Context, series, subject string, _, _ time.Time) ([]TrendPoint, error) {
	if f.trendErr != nil {
		return nil, f.trendErr
	}
	return f.points[series+"|"+subject], nil
}

var _ TrendSource = (*fakeTrendSource)(nil)

func newTestServerWithTrends(trends TrendSource) *Server {
	return NewServer(&fakeSource{}, NoopProposalSink{}, ServerOptions{TrendSource: trends})
}

func f64(v float64) *float64 { return &v }

func TestTrendToolsCategoryAndReadOnlyHint(t *testing.T) {
	s := newTestServerWithTrends(&fakeTrendSource{})
	resp := rpcCall(t, s, "tools/list", nil)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	found := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		if name != "list_trend_series" && name != "get_smart_trend" {
			continue
		}
		found[name] = true
		meta := tool["_meta"].(map[string]any)
		assert.Equal(t, "trends", meta["category"])
		annotations := tool["annotations"].(map[string]any)
		assert.Equal(t, true, annotations["readOnlyHint"])
	}
	assert.True(t, found["list_trend_series"], "list_trend_series not registered")
	assert.True(t, found["get_smart_trend"], "get_smart_trend not registered")
}

func TestTrendToolsAbsentWithoutTrendSource(t *testing.T) {
	s := newTestServer(&fakeSource{}) // ServerOptions{} — no TrendSource
	resp := rpcCall(t, s, "tools/list", nil)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	for _, raw := range tools {
		name := raw.(map[string]any)["name"].(string)
		assert.NotEqual(t, "get_smart_trend", name)
		assert.NotEqual(t, "list_trend_series", name)
	}
}

func TestListTrendSeriesReturnsStoredPairs(t *testing.T) {
	src := &fakeTrendSource{series: []TrendSeriesInfo{
		{Series: "smart.reallocated_sector_ct", Subject: "/dev/sdb"},
		{Series: "capacity.disk", Subject: "/dev/sdb"},
	}}
	s := newTestServerWithTrends(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "list_trend_series", "arguments": map[string]any{}})
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, text, "smart.reallocated_sector_ct")
	assert.Contains(t, text, "/dev/sdb")
	assert.Contains(t, text, "capacity.disk")
}

func TestGetSmartTrendSummary(t *testing.T) {
	now := time.Now().UTC()
	src := &fakeTrendSource{
		series: []TrendSeriesInfo{{Series: "smart.reallocated_sector_ct", Subject: "/dev/sdb"}},
		points: map[string][]TrendPoint{
			"smart.reallocated_sector_ct|/dev/sdb": {
				{At: now.Add(-2 * time.Hour), Value: f64(0), OK: true},
				{At: now.Add(-1 * time.Hour), Value: f64(8), OK: true},
			},
		},
	}
	s := newTestServerWithTrends(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_smart_trend",
		"arguments": map[string]any{"series": "smart.reallocated_sector_ct", "subject": "/dev/sdb", "hours": 24},
	})
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, false, result["isError"])
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)

	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &v))
	assert.Equal(t, float64(2), v["sample_count"])
	assert.Equal(t, float64(0), v["gap_count"])
	assert.Equal(t, false, v["gap_heavy"])
	assert.Equal(t, float64(0), v["first"])
	assert.Equal(t, float64(8), v["last"])
	assert.Equal(t, float64(8), v["delta"])
	assert.Equal(t, "up", v["direction"])
}

func TestGetSmartTrendDetailReturnsFullSeriesWithGaps(t *testing.T) {
	now := time.Now().UTC()
	src := &fakeTrendSource{
		series: []TrendSeriesInfo{{Series: "smart.reallocated_sector_ct", Subject: "/dev/sdb"}},
		points: map[string][]TrendPoint{
			"smart.reallocated_sector_ct|/dev/sdb": {
				{At: now.Add(-2 * time.Hour), Value: f64(0), OK: true},
				{At: now.Add(-1 * time.Hour), OK: false, Note: "disk in standby"},
			},
		},
	}
	s := newTestServerWithTrends(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_smart_trend",
		"arguments": map[string]any{"series": "smart.reallocated_sector_ct", "subject": "/dev/sdb", "detail": true},
	})
	require.Nil(t, resp["error"])
	text := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)

	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &v))
	points := v["points"].([]any)
	require.Len(t, points, 2)

	p0 := points[0].(map[string]any)
	assert.Equal(t, true, p0["ok"])
	assert.Equal(t, float64(0), p0["value"])

	p1 := points[1].(map[string]any)
	assert.Equal(t, false, p1["ok"])
	_, hasValue := p1["value"]
	assert.False(t, hasValue, "a gap must never carry a fake value")
	assert.Equal(t, "disk in standby", p1["note"])
}

func TestGetSmartTrendUnknownSeriesListsValidOptions(t *testing.T) {
	src := &fakeTrendSource{series: []TrendSeriesInfo{
		{Series: "smart.reallocated_sector_ct", Subject: "/dev/sdb"},
		{Series: "capacity.disk", Subject: "/dev/sdb"},
	}}
	s := newTestServerWithTrends(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_smart_trend",
		"arguments": map[string]any{"series": "smart.nonexistent_metric", "subject": "/dev/sdb"},
	})
	require.Nil(t, resp["error"], "unknown series is a Tool Execution Error, not a protocol error")
	result := resp["result"].(map[string]any)
	assert.Equal(t, true, result["isError"])
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, text, "unknown_series")
	assert.Contains(t, text, "smart.reallocated_sector_ct")
	assert.Contains(t, text, "capacity.disk")
}

func TestGetSmartTrendGapHeavyWindowIsNotAConfidentTrend(t *testing.T) {
	now := time.Now().UTC()
	var points []TrendPoint
	for i := range 40 {
		points = append(points, TrendPoint{At: now.Add(-time.Duration(i) * time.Minute), OK: false, Note: "disk in standby"})
	}
	points = append(points,
		TrendPoint{At: now.Add(-41 * time.Minute), Value: f64(0), OK: true},
		TrendPoint{At: now.Add(-42 * time.Minute), Value: f64(3), OK: true},
		TrendPoint{At: now.Add(-43 * time.Minute), Value: f64(8), OK: true},
	)
	src := &fakeTrendSource{
		series: []TrendSeriesInfo{{Series: "smart.reallocated_sector_ct", Subject: "/dev/sdb"}},
		points: map[string][]TrendPoint{"smart.reallocated_sector_ct|/dev/sdb": points},
	}
	s := newTestServerWithTrends(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_smart_trend",
		"arguments": map[string]any{"series": "smart.reallocated_sector_ct", "subject": "/dev/sdb"},
	})
	require.Nil(t, resp["error"])
	text := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)

	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &v))
	assert.Equal(t, float64(3), v["sample_count"])
	assert.Equal(t, float64(40), v["gap_count"])
	assert.Equal(t, true, v["gap_heavy"])
	assert.Equal(t, "insufficient-data", v["direction"],
		"3 samples against 40 gaps must never be reported as a confident up/down trend")
	assert.NotContains(t, v, "delta")
}

func TestGetSmartTrendBadWindowIsInvalidParams(t *testing.T) {
	src := &fakeTrendSource{series: []TrendSeriesInfo{{Series: "smart.reallocated_sector_ct", Subject: "/dev/sdb"}}}
	s := newTestServerWithTrends(src)

	// both hours and days
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_smart_trend",
		"arguments": map[string]any{"series": "smart.reallocated_sector_ct", "subject": "/dev/sdb", "hours": 24, "days": 1},
	})
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok, "expected a protocol error, got %+v", resp)
	assert.Equal(t, float64(-32602), errObj["code"])

	// negative hours
	resp = rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_smart_trend",
		"arguments": map[string]any{"series": "smart.reallocated_sector_ct", "subject": "/dev/sdb", "hours": -5},
	})
	errObj, ok = resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(-32602), errObj["code"])

	// beyond the retention cap
	resp = rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_smart_trend",
		"arguments": map[string]any{"series": "smart.reallocated_sector_ct", "subject": "/dev/sdb", "days": 365},
	})
	errObj, ok = resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestGetSmartTrendDetailTruncatesVisibly(t *testing.T) {
	now := time.Now().UTC()
	var points []TrendPoint
	for i := range 300 {
		points = append(points, TrendPoint{
			At: now.Add(-time.Duration(i) * time.Minute), Value: f64(float64(i)), OK: true,
			Note: fmt.Sprintf("reading %d from a fairly verbose sampler note field", i),
		})
	}
	src := &fakeTrendSource{
		series: []TrendSeriesInfo{{Series: "smart.reallocated_sector_ct", Subject: "/dev/sdb"}},
		points: map[string][]TrendPoint{"smart.reallocated_sector_ct|/dev/sdb": points},
	}
	s := newTestServerWithTrends(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_smart_trend",
		"arguments": map[string]any{"series": "smart.reallocated_sector_ct", "subject": "/dev/sdb", "detail": true},
	})
	require.Nil(t, resp["error"])
	text := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.LessOrEqualf(t, len(text), maxResultBytes+500, "truncated result should stay near the cap, got %d bytes", len(text))
	assert.True(t, strings.Contains(text, "TRUNCATED"), "expected a visible truncation marker, got: %s", text[:200])
}

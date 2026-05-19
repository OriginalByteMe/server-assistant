package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// ARK-9 acceptance: the dashboard renders a latency/Status trend sparkline per
// subject (Service and Host), server-rendered, no build step (ADR 0004).
func TestDashboard_RendersSparklinePerSubject(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	vs := &fakeVS{
		snap: []core.ServiceView{
			{Name: "plex", Status: core.StatusUp, Latency: 30 * time.Millisecond, LastChecked: now},
			{Name: "unraid", Status: core.StatusUp, Latency: 2 * time.Millisecond, LastChecked: now},
		},
		history: map[string][]core.ProbeSample{
			"plex": {
				{Service: "plex", Status: core.StatusUp, Latency: 10 * time.Millisecond, At: now.Add(-2 * time.Minute)},
				{Service: "plex", Status: core.StatusDegraded, Latency: 80 * time.Millisecond, At: now.Add(-time.Minute)},
				{Service: "plex", Status: core.StatusUp, Latency: 30 * time.Millisecond, At: now},
			},
			"unraid": {
				{Service: "unraid", Status: core.StatusUp, Latency: 2 * time.Millisecond, At: now},
			},
		},
	}

	rec := httptest.NewRecorder()
	Handler(vs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	// An inline SVG sparkline (no JS, no build) is rendered for each subject.
	require.Contains(t, body, "<svg", "a sparkline svg is rendered")
	require.GreaterOrEqual(t, countSub(body, "<polyline"), 2, "one trend polyline per subject (Service + Host)")
	require.Contains(t, body, "<th>Trend</th>", "the dashboard has a Trend column")
}

// A subject with no history still renders a row (a placeholder, never a broken
// or missing cell) so the table stays well-formed.
func TestDashboard_SparklineEmptyHistoryIsPlaceholder(t *testing.T) {
	vs := &fakeVS{
		snap:    []core.ServiceView{{Name: "fresh", Status: core.StatusUnknown}},
		history: map[string][]core.ProbeSample{}, // no samples yet
	}
	rec := httptest.NewRecorder()
	Handler(vs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `class="spark"`, "the spark cell is always present")
}

func countSub(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}

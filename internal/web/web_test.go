package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

type fakeVS struct {
	snap    []core.ServiceView
	ch      chan core.ServiceView
	history map[string][]core.ProbeSample
}

func (f *fakeVS) Snapshot() []core.ServiceView { return f.snap }
func (f *fakeVS) Subscribe() (<-chan core.ServiceView, func()) {
	return f.ch, func() {}
}
func (f *fakeVS) History(name string) []core.ProbeSample { return f.history[name] }

// The dashboard server-renders the Service list with Status, latency, and
// last-checked, wired for live updates via vendored HTMX + its SSE extension
// (AC #5; designated libraries — CONVENTIONS).
func TestDashboard_RendersServiceListWithHTMX(t *testing.T) {
	vs := &fakeVS{snap: []core.ServiceView{{
		Name: "plex", Status: core.StatusDegraded,
		Latency: 420 * time.Millisecond, LastChecked: time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC),
	}}}

	rec := httptest.NewRecorder()
	Handler(vs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `src="/static/htmx.min.js"`)
	require.Contains(t, body, `src="/static/sse.js"`)
	require.Contains(t, body, `hx-ext="sse"`)
	require.Contains(t, body, `sse-connect="/events"`)
	require.Contains(t, body, `sse-swap="svc-plex"`)
	require.Contains(t, body, "plex")
	require.Contains(t, body, "DEGRADED")
	require.Contains(t, body, "420 ms")
	require.Contains(t, body, "2026-05-17T12:00:00Z")
}

// The vendored HTMX asset is actually served (designated library is wired in,
// not just referenced).
func TestDashboard_ServesVendoredHTMX(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(&fakeVS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "htmx")
}

// The SSE stream emits the initial snapshot and pushes a committed change
// live as a named event whose data is the HTML row fragment HTMX swaps in
// (AC #6 — no client refresh).
func TestDashboard_SSEPushesHTMLFragment(t *testing.T) {
	vs := &fakeVS{
		snap: []core.ServiceView{{Name: "web", Status: core.StatusUp}},
		ch:   make(chan core.ServiceView, 1),
	}
	srv := httptest.NewServer(Handler(vs))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	sc := bufio.NewScanner(resp.Body)
	ev, data := readEvent(t, sc)
	require.Equal(t, "event: svc-web", ev)
	require.Contains(t, data, `<td class="status s-UP">UP</td>`) // HTML fragment, not JSON

	vs.ch <- core.ServiceView{Name: "web", Status: core.StatusDown}
	ev, data = readEvent(t, sc)
	require.Equal(t, "event: svc-web", ev)
	require.Contains(t, data, `s-DOWN`)
}

// readEvent reads scanner lines until it has an SSE event/data pair.
func readEvent(t *testing.T, sc *bufio.Scanner) (event, data string) {
	t.Helper()
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = line
		case strings.HasPrefix(line, "data: "):
			return event, strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatal("no SSE data line before stream ended")
	return "", ""
}

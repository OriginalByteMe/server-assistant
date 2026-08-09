package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// ADR 0005 acceptance: the dashboard must render UNKNOWN ("can't tell")
// distinctly from DOWN ("confirmed dead"). A blind, gated Service carries the
// s-UNKNOWN class with its own colour rule — never the s-DOWN one — so the
// Operator can see "we're blind" at a glance and never mistakes it for an
// outage. The Host is a first-class subject row alongside its Services.
func TestDashboard_UnknownRendersDistinctlyFromDown(t *testing.T) {
	vs := &fakeVS{snap: []core.ServiceView{
		{Name: "unraid", Status: core.StatusDown, LastChecked: time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)},
		{Name: "plex", Status: core.StatusUnknown, LastChecked: time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)},
	}}

	rec := httptest.NewRecorder()
	Handler(vs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// The Host renders as its own subject row, wired for live SSE updates.
	require.Contains(t, body, `sse-swap="svc-unraid"`)
	require.Contains(t, body, "unraid")

	// The blind Service carries UNKNOWN, never DOWN.
	require.Contains(t, body, `s-UNKNOWN">UNKNOWN`)
	require.NotContains(t, body, `s-UNKNOWN">DOWN`)

	// The two states have distinct style rules — not the same colour. The
	// rules live in the shared stylesheet the page links, served by the
	// same handler.
	require.Contains(t, body, `href="/static/style.css"`)
	rec = httptest.NewRecorder()
	Handler(vs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/style.css", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	css := rec.Body.String()
	require.Contains(t, css, ".s-UNKNOWN {")
	require.Contains(t, css, ".s-DOWN {")
	require.NotContains(t, css, ".s-UNKNOWN, .s-DOWN", "UNKNOWN and DOWN must not share one rule")
}

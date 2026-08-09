package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeCommandSource is the CommandSource test double. runErr/results let a
// test script a specific outcome (success, application-level failure, or a
// hard backend error) per call, mirroring fakeUnraidSource's
// independently-settable-error convention.
type fakeCommandSource struct {
	commands    []Command
	commandsErr error
	result      CommandResult
	runErr      error
	runs        []string // ids Run was actually called with
	runDeadline time.Duration
}

var _ CommandSource = (*fakeCommandSource)(nil)

func (f *fakeCommandSource) Commands(context.Context) ([]Command, error) {
	return f.commands, f.commandsErr
}

func (f *fakeCommandSource) Run(ctx context.Context, id, _ string) (CommandResult, error) {
	f.runs = append(f.runs, id)
	if dl, ok := ctx.Deadline(); ok {
		f.runDeadline = time.Until(dl)
	}
	return f.result, f.runErr
}

// Regression (live, on rijkaardserver): the run handler used to share
// handlerTimeout, the 5s ceiling sized for READ handlers. A real container
// restart takes longer than that — a busybox container ignoring SIGTERM
// alone burns ~5s before the engine SIGKILLs it — so every genuine restart
// came back "context deadline exceeded" while the restart actually
// succeeded on the host, and the configured commands.timeout was
// unreachable because the outer context always won.
func TestCommandRun_GetsMoreThanTheReadHandlerBudget(t *testing.T) {
	cs := &fakeCommandSource{
		commands: []Command{{ID: "restart-container:sa-demo-web", Label: "Restart sa-demo-web"}},
		result:   CommandResult{OK: true, Output: "restarted", StartedAt: time.Now(), FinishedAt: time.Now()},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/unraid/commands/restart-container:sa-demo-web/run", nil)
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Greater(t, cs.runDeadline, handlerTimeout,
		"a mutating command must not inherit the read-handler timeout")
}

// The consequence line must be visible before any click — this tier has no
// second approval gate, so the page load itself (no POST yet) must already
// carry it.
func TestUnraidPage_CommandConsequencePresentBeforeAnyClick(t *testing.T) {
	cs := &fakeCommandSource{commands: []Command{
		{ID: "restart-container:sa-demo-web", Label: "Restart sa-demo-web", Description: "Restarts the demo container.", Consequence: "Stops and restarts the sa-demo-web container; briefly interrupts it."},
	}}

	rec := httptest.NewRecorder()
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, "Restart sa-demo-web")
	require.Contains(t, body, "Stops and restarts the sa-demo-web container; briefly interrupts it.")
	require.Empty(t, cs.runs, "consequence must render without Run ever being called")
	require.Contains(t, body, `hx-post="/api/unraid/commands/restart-container:sa-demo-web/run"`)
}

// An empty catalog (the deployed default per issue #51's risk register)
// renders an explicit, friendly empty state — never a blank/broken-looking
// panel.
func TestUnraidPage_EmptyCommandCatalogRendersExplicitEmptyState(t *testing.T) {
	cs := &fakeCommandSource{commands: nil}

	rec := httptest.NewRecorder()
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, "Commands")
	require.Contains(t, body, "No commands are configured")
	require.Contains(t, body, "config.docker.yaml")
	require.NotContains(t, body, "command-row")
}

// A read error on the catalog itself is surfaced honestly (rule 5) rather
// than collapsing into the empty-state text.
func TestUnraidPage_CommandCatalogErrorSurfacedNotEmptyState(t *testing.T) {
	cs := &fakeCommandSource{commandsErr: errors.New("catalog store unavailable")}

	rec := httptest.NewRecorder()
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, "catalog store unavailable")
	require.NotContains(t, body, "No commands are configured")
}

// With no CommandSource wired in, the Commands panel is entirely absent and
// the run route 404s — same nil-means-absent convention as Harness/Proposals.
func TestUnraidPage_NilCommandSourceSectionAbsentRouteAbsent(t *testing.T) {
	h := HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "<h2>Commands</h2>")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/unraid/commands/anything/run", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// Also confirm HandlerFull itself (no cs param at all) still omits the panel
// and the route — HandlerFull's existing exported signature is unchanged.
func TestUnraidPage_HandlerFullOmitsCommandsPanel(t *testing.T) {
	h := HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "<h2>Commands</h2>")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/unraid/commands/anything/run", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// A successful run renders the real output and timing — never a generic
// "done" — and the JSON response (non-HTMX caller) carries the real
// CommandResult. Only the ID travels: no body/form field selects a target.
func TestAPICommandRun_SuccessRendersRealOutputAndTiming(t *testing.T) {
	start := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cs := &fakeCommandSource{
		commands: []Command{{ID: "restart-container:sa-demo-web", Label: "Restart sa-demo-web", Consequence: "Restarts the demo container."}},
		result:   CommandResult{OK: true, Output: "container sa-demo-web restarted (id 9f2a1c)", StartedAt: start, FinishedAt: start.Add(340 * time.Millisecond)},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/unraid/commands/restart-container:sa-demo-web/run", nil)
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"restart-container:sa-demo-web"}, cs.runs)

	var got commandResultDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.True(t, got.OK)
	require.Equal(t, "container sa-demo-web restarted (id 9f2a1c)", got.Output)
	require.NotEmpty(t, got.StartedAt)
	require.NotEmpty(t, got.FinishedAt)
}

// An HTMX-originated request (HX-Request: true) gets the re-rendered row
// fragment instead of JSON, so the outcome swaps into the panel in place.
func TestAPICommandRun_HTMXRequestRendersRowFragment(t *testing.T) {
	cs := &fakeCommandSource{
		commands: []Command{{ID: "restart-container:sa-demo-web", Label: "Restart sa-demo-web", Consequence: "Restarts the demo container."}},
		result:   CommandResult{OK: true, Output: "restarted", StartedAt: time.Now(), FinishedAt: time.Now()},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/unraid/commands/restart-container:sa-demo-web/run", nil)
	req.Header.Set("HX-Request", "true")
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, "Restart sa-demo-web")
	require.Contains(t, body, "restarted")
	require.Contains(t, body, "Succeeded")
	require.Contains(t, body, `id="cmd-restart-container:sa-demo-web"`)
}

// A failed run (CommandResult.OK == false) surfaces the real error text —
// never a success indication and never a generic "done".
func TestAPICommandRun_ApplicationFailureRendersRealError(t *testing.T) {
	cs := &fakeCommandSource{
		commands: []Command{{ID: "restart-container:sa-demo-web", Label: "Restart sa-demo-web"}},
		result:   CommandResult{OK: false, Output: "docker restart failed: container not found", StartedAt: time.Now(), FinishedAt: time.Now()},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/unraid/commands/restart-container:sa-demo-web/run", nil)
	req.Header.Set("HX-Request", "true")
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, "docker restart failed: container not found")
	require.Contains(t, body, "Failed")
	require.NotContains(t, body, "Succeeded")

	var dto commandResultDTO
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/unraid/commands/restart-container:sa-demo-web/run", nil)
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec2, req2)
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &dto))
	require.False(t, dto.OK)
	require.Equal(t, "docker restart failed: container not found", dto.Output)
}

// A hard backend error (e.g. an unknown ID, per contract: "an unknown ID is
// an error, never a pass-through") renders identically to an application
// failure — the real error text, never swallowed or genericized.
func TestAPICommandRun_BackendErrorRendersRealErrorNotGenericFailure(t *testing.T) {
	cs := &fakeCommandSource{
		commands: nil, // catalog doesn't even know this id
		runErr:   errors.New("unknown command id"),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/unraid/commands/does-not-exist/run", nil)
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var dto commandResultDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &dto))
	require.False(t, dto.OK)
	require.Equal(t, "unknown command id", dto.Output)
}

// GET is rejected on the run route, matching handleAPIProposalDecision's
// method guard.
func TestAPICommandRun_MethodNotAllowedOnGET(t *testing.T) {
	cs := &fakeCommandSource{commands: []Command{{ID: "x"}}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/unraid/commands/x/run", nil)
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// The unauthenticated-dashboard banner must remain visible above the
// Commands panel — this panel is the first surface that mutates anything,
// so the warning matters more here, not less.
func TestUnraidPage_UnauthBannerPresentAboveCommandsPanel(t *testing.T) {
	cs := &fakeCommandSource{commands: []Command{{ID: "x", Label: "X", Consequence: "does x"}}}
	rec := httptest.NewRecorder()
	HandlerComplete(&fakeVS{}, nil, fullFakeUnraidSource(), nil, cs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	body := rec.Body.String()

	require.Contains(t, body, "alert-unauth")
	require.Contains(t, body, "unauthenticated")
	require.Less(t, strings.Index(body, "alert-unauth"), strings.Index(body, "<h2>Commands</h2>"))
}

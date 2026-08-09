package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

var errUnknownIncident = errors.New("unknown incident")

// fakeDecision records one Approve/Deny call for test assertions.
type fakeDecision struct {
	kind string
	id   string
	who  string
}

// fakeHS is the HarnessSource test double, shared by incidents_test.go and
// api_test.go (same package).
type fakeHS struct {
	mode      core.HarnessMode
	halted    bool
	reason    string
	cycles    map[string]core.HarnessCycle
	order     []string // Incidents() iteration order, caller-controlled
	decisions []fakeDecision
}

func newFakeHS(cycles ...core.HarnessCycle) *fakeHS {
	f := &fakeHS{cycles: map[string]core.HarnessCycle{}}
	for _, c := range cycles {
		f.cycles[c.ID] = c
		f.order = append(f.order, c.ID)
	}
	return f
}

func (f *fakeHS) Mode() core.HarnessMode { return f.mode }
func (f *fakeHS) Halted() bool           { return f.halted }
func (f *fakeHS) Halt(reason string)     { f.halted = true; f.reason = reason }
func (f *fakeHS) Rearm()                 { f.halted = false }

func (f *fakeHS) Incidents(_ context.Context, limit int) ([]core.HarnessCycle, error) {
	out := make([]core.HarnessCycle, 0, len(f.order))
	for _, id := range f.order {
		if len(out) >= limit {
			break
		}
		out = append(out, f.cycles[id])
	}
	return out, nil
}

func (f *fakeHS) Incident(_ context.Context, id string) (core.HarnessCycle, error) {
	c, ok := f.cycles[id]
	if !ok {
		return core.HarnessCycle{}, errUnknownIncident
	}
	return c, nil
}

func (f *fakeHS) decide(kind, id, who string, approval core.ApprovalDecision) error {
	c, ok := f.cycles[id]
	if !ok {
		return errUnknownIncident
	}
	c.Approval = approval
	c.ApprovedBy = who
	c.ApprovedAt = time.Now()
	f.cycles[id] = c
	f.decisions = append(f.decisions, fakeDecision{kind: kind, id: id, who: who})
	return nil
}

func (f *fakeHS) Approve(_ context.Context, id, who string) error {
	return f.decide("approve", id, who, core.ApprovalApproved)
}

func (f *fakeHS) Deny(_ context.Context, id, who string) error {
	return f.decide("deny", id, who, core.ApprovalDenied)
}

// The Incidents list renders one row per HarnessCycle (AC: newest-first
// list from the harness's own accountability record, ADR 0019).
func TestIncidents_ListRendersRowPerCycle(t *testing.T) {
	hs := newFakeHS(
		core.HarnessCycle{ID: "c1", Subject: "sa-demo-web", TriggerStatus: core.StatusDown, Mode: core.HarnessLive},
		core.HarnessCycle{ID: "c2", Subject: "plex", TriggerStatus: core.StatusDown, Mode: core.HarnessLive},
	)

	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/incidents", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `/incidents/c1`)
	require.Contains(t, body, `/incidents/c2`)
	require.Contains(t, body, "sa-demo-web")
	require.Contains(t, body, "plex")
}

// A pending incident's detail page offers Approve and Deny; a decided one
// shows the recorded decision instead (ADR 0009/0023 Approval gate).
func TestIncidentDetail_PendingShowsApproveDenyDecidedDoesNot(t *testing.T) {
	hs := newFakeHS(
		core.HarnessCycle{ID: "pending", Subject: "sa-demo-web", TriggerStatus: core.StatusDown, Approval: core.ApprovalPending},
		core.HarnessCycle{ID: "decided", Subject: "sa-demo-web", TriggerStatus: core.StatusDown, Approval: core.ApprovalApproved, ApprovedBy: "noah", ApprovedAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)},
	)
	vs := &fakeVS{}

	rec := httptest.NewRecorder()
	HandlerWithHarness(vs, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/incidents/pending", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), ">Approve<")
	require.Contains(t, rec.Body.String(), ">Deny<")

	rec = httptest.NewRecorder()
	HandlerWithHarness(vs, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/incidents/decided", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, ">Approve<")
	require.NotContains(t, body, ">Deny<")
	require.Contains(t, body, "approved")
	require.Contains(t, body, "noah")
}

// An unknown incident id 404s rather than panicking or rendering an empty
// page.
func TestIncidentDetail_UnknownIDIs404(t *testing.T) {
	hs := newFakeHS()
	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/incidents/nope", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// Tool output is untrusted evidence scrubbed for secrets but not for HTML —
// html/template must still escape it verbatim in the Facts block, never
// rendered via template.HTML.
func TestIncidentDetail_ToolOutputIsHTMLEscaped(t *testing.T) {
	const payload = `<script>alert(1)</script>`
	hs := newFakeHS(core.HarnessCycle{
		ID:      "xss",
		Subject: "sa-demo-web",
		ToolCalls: []core.ToolCall{
			{Tool: "container_status", Output: payload, At: time.Now()},
		},
	})

	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/incidents/xss", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
	require.NotContains(t, body, payload)
}

// With no HarnessSource wired in, the existing dashboard keeps working and
// the harness routes are simply absent (404), not a panic or a broken page.
func TestNilHarnessSource_DashboardOKIncidents404(t *testing.T) {
	vs := &fakeVS{snap: []core.ServiceView{{Name: "plex", Status: core.StatusUp}}}
	h := Handler(vs)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "<h2>Harness</h2>")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/incidents", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// With a HarnessSource wired in, the dashboard renders the Harness panel:
// mode, halted state, and the Halt/Re-arm controls (ADR 0020 asymmetry —
// Re-arm alone carries a confirm()).
func TestDashboard_RendersHarnessPanel(t *testing.T) {
	hs := newFakeHS()
	hs.mode = core.HarnessLive
	hs.halted = true

	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "<h2>Harness</h2>")
	require.Contains(t, body, "HALTED")
	require.Contains(t, body, "live")
	require.Contains(t, body, `action="/api/harness/halt"`)
	require.Contains(t, body, `action="/api/harness/rearm"`)
	require.Contains(t, body, `onsubmit="return confirm(`)
	require.NotContains(t, body, `<form method="post" action="/api/harness/halt" onsubmit`, "Halt must be one click, no confirm — ADR 0020 asymmetry")
}

// A cycle awaiting Approval surfaces as a top-of-dashboard alert banner
// naming the subject and proposed action and linking to the detail page
// (ADR 0023 — an unattended incident must be impossible to miss). With no
// pending cycle there is no banner.
func TestDashboard_PendingIncidentBanner(t *testing.T) {
	pending := core.HarnessCycle{
		ID:            "c-pending",
		Subject:       "sa-demo-web",
		TriggerStatus: core.StatusDown,
		Approval:      core.ApprovalPending,
		Diagnosis:     core.Diagnosis{Proposed: core.ProposedAction{Kind: "restart_container", Subject: "sa-demo-web"}},
	}
	decided := core.HarnessCycle{
		ID:       "c-decided",
		Subject:  "plex",
		Approval: core.ApprovalApproved,
	}

	tests := []struct {
		name   string
		hs     *fakeHS
		banner bool
	}{
		{"pending cycle renders banner", newFakeHS(pending, decided), true},
		{"no pending cycle omits banner", newFakeHS(decided), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			HandlerWithHarness(&fakeVS{}, tc.hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			if !tc.banner {
				require.NotContains(t, body, `class="alert-pending"`)
				return
			}
			require.Contains(t, body, `class="alert-pending"`)
			require.Contains(t, body, "sa-demo-web")
			require.Contains(t, body, "restart_container")
			require.Contains(t, body, `/incidents/c-pending`)
		})
	}
}

// A tool call that ran but produced nothing renders an explicit "(no
// output)" marker — distinguishable from a broken page — while non-empty
// output keeps its verbatim <pre> block.
func TestIncidentDetail_EmptyToolOutputRendersNoOutput(t *testing.T) {
	hs := newFakeHS(core.HarnessCycle{
		ID:      "quiet",
		Subject: "sa-demo-web",
		ToolCalls: []core.ToolCall{
			{Tool: "container_logs", Output: "", At: time.Now()},
			{Tool: "container_status", Output: "Up 2 minutes", At: time.Now()},
		},
	})

	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/incidents/quiet", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "(no output)")
	require.Contains(t, body, "Up 2 minutes")
}

// A failed dispatch reads as an explicit error, never as the neutral
// "Dispatched at: —" that looks like nothing happened (display only — the
// stored cycle is untouched).
func TestIncidentDetail_FailedDispatchStyledAsError(t *testing.T) {
	hs := newFakeHS(core.HarnessCycle{
		ID:             "boom",
		Subject:        "sa-demo-web",
		Approval:       core.ApprovalApproved,
		ApprovedBy:     "noah",
		DispatchResult: "ssh: exit status 77",
		Outcome:        core.OutcomeDispatchErr,
	})

	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/incidents/boom", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Dispatch attempted and failed")
	require.Contains(t, body, "ssh: exit status 77")
	require.NotContains(t, body, "Dispatched at: —")
}

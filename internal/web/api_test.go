package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

var apiContractKeys = []string{
	"id", "subject", "trigger_status", "mode", "started_at", "tool_calls",
	"diagnosis", "approval", "approved_by", "approved_at", "resolved_target",
	"dispatch_result", "dispatched_at", "outcome", "outcome_at", "error",
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// GET /api/incidents emits exactly the contract's keys, with a zero
// ApprovedAt marshalled as JSON null and durations as _ms integers.
func TestAPIIncidents_EmitsContractKeysAndNullApprovedAt(t *testing.T) {
	hs := newFakeHS(core.HarnessCycle{
		ID:            "c1",
		Subject:       "sa-demo-web",
		TriggerStatus: core.StatusDown,
		Mode:          core.HarnessLive,
		StartedAt:     time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		ToolCalls: []core.ToolCall{
			{Tool: "container_status", At: time.Date(2026, 8, 8, 12, 0, 1, 0, time.UTC), Duration: 250 * time.Millisecond},
		},
		Diagnosis: core.Diagnosis{
			Proposed: core.ProposedAction{Kind: core.ActionRestartContainer, Subject: "sa-demo-web"},
			Usage:    core.Usage{Backend: "ollama", Latency: 900 * time.Millisecond},
		},
		// ApprovedAt / DispatchedAt / OutcomeAt intentionally left zero.
	})

	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/incidents", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)

	elem := got[0]
	require.ElementsMatch(t, apiContractKeys, keysOf(elem))
	require.Nil(t, elem["approved_at"])
	require.Nil(t, elem["dispatched_at"])
	require.Nil(t, elem["outcome_at"])
	require.NotNil(t, elem["started_at"])

	calls, ok := elem["tool_calls"].([]any)
	require.True(t, ok)
	require.Len(t, calls, 1)
	call := calls[0].(map[string]any)
	require.Equal(t, float64(250), call["duration_ms"])

	diagnosis := elem["diagnosis"].(map[string]any)
	usage := diagnosis["usage"].(map[string]any)
	require.Equal(t, float64(900), usage["latency_ms"])
}

// Approve is POST-only.
func TestAPIApprove_MethodNotAllowedOnGET(t *testing.T) {
	hs := newFakeHS(core.HarnessCycle{ID: "c1", Approval: core.ApprovalPending})
	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/incidents/c1/approve", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// Approving an unknown incident 404s.
func TestAPIApprove_UnknownIDIs404(t *testing.T) {
	hs := newFakeHS()
	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/incidents/nope/approve", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// Approving an already-decided incident conflicts.
func TestAPIApprove_NonPendingIs409(t *testing.T) {
	hs := newFakeHS(core.HarnessCycle{ID: "c1", Approval: core.ApprovalApproved})
	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/incidents/c1/approve", nil))
	require.Equal(t, http.StatusConflict, rec.Code)
}

// A successful approve returns 200 with the updated incident, and the who
// is passed through to the HarnessSource.
func TestAPIApprove_SuccessRecordsWhoAndReturns200(t *testing.T) {
	hs := newFakeHS(core.HarnessCycle{ID: "c1", Approval: core.ApprovalPending})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/incidents/c1/approve?who=alice", nil)
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "approved", got["approval"])
	require.Equal(t, "alice", got["approved_by"])

	require.Len(t, hs.decisions, 1)
	require.Equal(t, fakeDecision{kind: "approve", id: "c1", who: "alice"}, hs.decisions[0])
}

// Deny mirrors Approve for the second route sharing handleAPIDecision.
func TestAPIDeny_SuccessRecordsWhoAndReturns200(t *testing.T) {
	hs := newFakeHS(core.HarnessCycle{ID: "c1", Approval: core.ApprovalPending})
	rec := httptest.NewRecorder()
	body := strings.NewReader("who=bob")
	req := httptest.NewRequest(http.MethodPost, "/api/incidents/c1/deny", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, hs.decisions, 1)
	require.Equal(t, fakeDecision{kind: "deny", id: "c1", who: "bob"}, hs.decisions[0])
}

// /api/health is always registered and nil-safe.
func TestAPIHealth_ReportsModeAndHalted(t *testing.T) {
	hs := newFakeHS()
	hs.mode = core.HarnessLive
	hs.halted = true

	rec := httptest.NewRecorder()
	HandlerWithHarness(&fakeVS{}, hs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "ok", got["status"])
	require.Equal(t, "live", got["harness_mode"])
	require.Equal(t, true, got["harness_halted"])

	rec = httptest.NewRecorder()
	Handler(&fakeVS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	got = nil
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "off", got["harness_mode"])
	require.Equal(t, false, got["harness_halted"])
}

// Halt and Rearm flip the HarnessSource's state.
func TestAPIHaltRearm_FlipsFakeState(t *testing.T) {
	hs := newFakeHS()
	h := HandlerWithHarness(&fakeVS{}, hs)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/harness/halt", strings.NewReader("reason=demo"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, hs.Halted())
	require.Equal(t, "demo", hs.reason)
	require.JSONEq(t, `{"halted":true}`, rec.Body.String())

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/harness/rearm", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, hs.Halted())
	require.JSONEq(t, `{"halted":false}`, rec.Body.String())
}

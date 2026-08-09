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

	"server-assistant/internal/core"
)

// fakeUnraidSource is the core.UnraidSource test double. Each method's
// error is independently settable so a test can fail exactly one section
// while the others keep succeeding — mirrors the real Host, where "the
// array read failed" and "the container list failed" are unrelated facts.
type fakeUnraidSource struct {
	host    core.HostInfo
	hostErr error

	array    core.ArrayState
	arrayErr error

	shares    []core.Share
	sharesErr error

	containers    []core.Container
	containersErr error

	smart    core.SmartAttrs
	smartErr error

	reach    core.Reachability
	reachErr error
}

var _ core.UnraidSource = (*fakeUnraidSource)(nil)

func (f *fakeUnraidSource) HostInfo(context.Context) (core.HostInfo, error) {
	return f.host, f.hostErr
}
func (f *fakeUnraidSource) Array(context.Context) (core.ArrayState, error) {
	return f.array, f.arrayErr
}
func (f *fakeUnraidSource) Shares(context.Context) ([]core.Share, error) {
	return f.shares, f.sharesErr
}
func (f *fakeUnraidSource) Containers(context.Context) ([]core.Container, error) {
	return f.containers, f.containersErr
}
func (f *fakeUnraidSource) SmartFor(context.Context, string) (core.SmartAttrs, error) {
	return f.smart, f.smartErr
}
func (f *fakeUnraidSource) Reachability(context.Context) (core.Reachability, error) {
	return f.reach, f.reachErr
}

// fullFakeUnraidSource returns a fake with every section populated and no
// errors — the "everything is healthy" baseline several tests start from.
func fullFakeUnraidSource() *fakeUnraidSource {
	temp := 38
	return &fakeUnraidSource{
		host: core.HostInfo{
			Hostname: "rijkaardserver", UnraidVersion: "7.3.2", CPUModel: "Ryzen 7 5800H",
			CPUCores: 8, CPUPercent: 12.5, MemTotalBytes: 34359738368, MemUsedBytes: 8589934592,
			UptimeSeconds: 90000, CollectedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		},
		array: core.ArrayState{
			State: "STARTED", ParityCheckActive: false, ParityLastErrors: 0,
			Disks: []core.Disk{
				{Name: "disk1", Device: "/dev/sdb", Role: "data", SizeBytes: 4000000000000, UsedBytes: 1000000000000, TempC: &temp, SmartStatus: "OK"},
			},
			CollectedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		},
		shares: []core.Share{
			{Name: "media", SizeBytes: 4000000000000, FreeBytes: 3000000000000, UsedBytes: 1000000000000, Allocator: "highwater", CachePool: "cache", Exported: true, Accessible: true},
		},
		containers: []core.Container{
			{Name: "plex", Image: "plexinc/pms-docker", State: "running", Status: "Up 16 days", Ports: []string{"32400:32400"}, AutoRun: true},
		},
		reach: core.Reachability{State: core.ReachTailnet, TailnetURL: "https://100.90.134.29", Detail: "reachable over tailscale", CollectedAt: time.Now()},
	}
}

// Every new route responds 200 against a fully-populated fake source (AC:
// "each new route returns 200 with a fake source").
func TestUnraidRoutes_AllReturn200WithFakeSource(t *testing.T) {
	h := HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), nil)

	for _, path := range []string{"/unraid", "/api/unraid/host", "/api/unraid/array", "/api/unraid/shares", "/api/unraid/containers"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equalf(t, http.StatusOK, rec.Code, "GET %s", path)
	}
}

// With no core.UnraidSource wired in, /unraid and its JSON mirror are
// simply absent (404), matching the nil-hs convention (unknown_test.go /
// incidents_test.go).
func TestUnraidRoutes_NilSourceRoutesAbsent(t *testing.T) {
	h := Handler(&fakeVS{})
	for _, path := range []string{"/unraid", "/api/unraid/host"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equalf(t, http.StatusNotFound, rec.Code, "GET %s", path)
	}
}

// core.ErrUnauthenticated on any section renders the explicit "not
// authenticated" hint in place of that section — never zeroed metrics
// standing in for real data (CONVENTIONS rule 5).
func TestUnraidPage_ErrUnauthenticatedRendersExplicitMessageNotZeros(t *testing.T) {
	us := fullFakeUnraidSource()
	us.hostErr = core.ErrUnauthenticated

	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, us, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, "Not authenticated against the Unraid API")
	require.Contains(t, body, "awaiting approval")
	// The failed Host section must not render a zeroed CPU/memory reading.
	require.NotContains(t, body, "0.0%")
	require.NotContains(t, body, "rijkaardserver") // the real hostname never appears — the section didn't render at all
}

// The JSON mirror maps core.ErrUnauthenticated to 401 with a structured
// body — never a 200 with an empty/zeroed object.
func TestUnraidAPI_ErrUnauthenticatedIs401WithStructuredBody(t *testing.T) {
	us := fullFakeUnraidSource()
	us.hostErr = core.ErrUnauthenticated

	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, us, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/unraid/host", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "unauthenticated", got["error"])
	require.NotEmpty(t, got["detail"])
}

// A generic (non-auth) UnraidSource failure maps to 502, distinct from the
// 401 unauthenticated case.
func TestUnraidAPI_GenericErrorIs502(t *testing.T) {
	us := fullFakeUnraidSource()
	us.arrayErr = errors.New("smartctl: device busy")

	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, us, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/unraid/array", nil))
	require.Equal(t, http.StatusBadGateway, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "unreachable", got["error"])
	require.Equal(t, "smartctl: device busy", got["detail"])
}

// A spun-down disk renders "spun down, not woken" — never a fake 0°C
// (core.ErrDiskStandby's own contract, CONVENTIONS rule 5).
func TestUnraidPage_StandbyDiskRendersAsStandbyNeverZeroTemp(t *testing.T) {
	us := fullFakeUnraidSource()
	us.array.Disks = []core.Disk{
		{Name: "disk2", Device: "/dev/sdc", Role: "data", SizeBytes: 4000000000000, UsedBytes: 500000000000, TempC: nil, SmartStatus: "OK", SpunDown: true},
	}

	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, us, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, "spun down, not woken")
	require.NotContains(t, body, "0°C")
}

// The JSON mirror keeps a spun-down disk's temp_c as JSON null, never 0.
func TestUnraidAPI_StandbyDiskTempCIsJSONNull(t *testing.T) {
	us := fullFakeUnraidSource()
	us.array.Disks = []core.Disk{
		{Name: "disk2", Role: "data", TempC: nil, SpunDown: true},
	}

	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, us, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/unraid/array", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	disks := got["disks"].([]any)
	require.Len(t, disks, 1)
	disk := disks[0].(map[string]any)
	require.Nil(t, disk["temp_c"])
	require.Equal(t, true, disk["spun_down"])
}

// All four core.ReachState values render distinctly — the dashboard must
// never collapse them into one "ok/broken" signal (GitHub #51/#56), and the
// tailnet state must warn plainly that a cloud LLM cannot reach it.
func TestUnraidPage_AllFourReachabilityStatesRenderDistinctly(t *testing.T) {
	cases := []struct {
		state    core.ReachState
		wantCSS  string
		wantText string
	}{
		{core.ReachAbsent, "reach-absent", "No Tailscale on this Host"},
		{core.ReachTailnet, "reach-tailnet", "a cloud-hosted LLM cannot reach this endpoint"},
		{core.ReachFunnel, "reach-funnel", "Served publicly via Tailscale Funnel"},
		{core.ReachFailing, "reach-failing", "Configured endpoint is not answering"},
	}

	seen := map[string]bool{}
	for _, c := range cases {
		us := fullFakeUnraidSource()
		us.reach = core.Reachability{State: c.state}

		rec := httptest.NewRecorder()
		HandlerFull(&fakeVS{}, nil, us, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()

		require.Containsf(t, body, c.wantCSS, "state %s", c.state)
		require.Containsf(t, body, c.wantText, "state %s", c.state)
		require.False(t, seen[c.wantText], "reachability text reused across states")
		seen[c.wantText] = true
	}
}

// The unauthenticated-dashboard banner is persistent — present regardless
// of Unraid state, since it documents that HL-SA-10 (login) is on hold, not
// that a particular read failed.
func TestUnraidPage_UnauthenticatedDashboardBannerAlwaysPresent(t *testing.T) {
	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "No authentication")
	require.Contains(t, rec.Body.String(), "unauthenticated")
}

// --- Approval surface for script proposals ---

var errUnknownProposal = errors.New("unknown proposal")

// fakeProposalSource is the ProposalSource test double, reusing
// incidents_test.go's fakeDecision shape (kind/id/who) since the recorded
// fact is identical.
type fakeProposalSource struct {
	proposals map[string]Proposal
	order     []string
	decisions []fakeDecision
}

var _ ProposalSource = (*fakeProposalSource)(nil)

func newFakeProposalSource(props ...Proposal) *fakeProposalSource {
	f := &fakeProposalSource{proposals: map[string]Proposal{}}
	for _, p := range props {
		f.proposals[p.ID] = p
		f.order = append(f.order, p.ID)
	}
	return f
}

func (f *fakeProposalSource) Proposals(context.Context) ([]Proposal, error) {
	out := make([]Proposal, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, f.proposals[id])
	}
	return out, nil
}

func (f *fakeProposalSource) Proposal(_ context.Context, id string) (Proposal, error) {
	p, ok := f.proposals[id]
	if !ok {
		return Proposal{}, errUnknownProposal
	}
	return p, nil
}

func (f *fakeProposalSource) decide(kind, id, who, decision string) error {
	p, ok := f.proposals[id]
	if !ok {
		return errUnknownProposal
	}
	p.Decision = decision
	p.DecidedBy = who
	p.DecidedAt = time.Now()
	f.proposals[id] = p
	f.decisions = append(f.decisions, fakeDecision{kind: kind, id: id, who: who})
	return nil
}

func (f *fakeProposalSource) Approve(_ context.Context, id, who string) error {
	return f.decide("approve", id, who, "approved")
}

func (f *fakeProposalSource) Deny(_ context.Context, id, who string) error {
	return f.decide("deny", id, who, "denied")
}

// The proposal's dry-run transcript is captioned "would do", never "will
// do" — a dry run has not executed anything (per-item requirement).
func TestUnraidPage_ProposalWouldDoWordingPresentNeverWillDo(t *testing.T) {
	ps := newFakeProposalSource(Proposal{
		ID: "p1", Title: "restart plex", Script: "docker restart plex",
		DryRunOutput: "would run: docker restart plex", RequestedBy: "mcp-tool", RequestedAt: time.Now(),
	})

	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), ps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, "would do")
	require.NotContains(t, body, "will do")
	require.Contains(t, body, "restart plex")
	require.Contains(t, body, "would run: docker restart plex")
	require.Contains(t, body, `action="/api/unraid/proposals/p1/approve"`)
	require.Contains(t, body, `action="/api/unraid/proposals/p1/deny"`)
}

// The MCP tool hands the model a dashboard_url of the form
// ".../unraid#proposal-<id>" (cmd/server-assistant/scripts_wiring.go's
// dashboardURLFor), and the model hands that straight to a human. Until
// 2026-08-09 the page rendered no matching element id at all, so every one
// of those links landed at the top of the page and the human had to hunt
// for the right proposal among all the pending ones. Found live, with two
// proposals already on the page.
func TestUnraidPage_ProposalHasAnchorMatchingDashboardURLFragment(t *testing.T) {
	ps := newFakeProposalSource(
		Proposal{ID: "aaa111", Title: "first", DryRunOutput: "would do a"},
		Proposal{ID: "bbb222", Title: "second", DryRunOutput: "would do b"},
	)
	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), ps).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `id="proposal-aaa111"`)
	require.Contains(t, body, `id="proposal-bbb222"`)
}

// With no ProposalSource wired in, the proposals section is absent and the
// decision routes 404 — same nil-means-absent convention as Harness.
func TestUnraidPage_NilProposalSourceSectionAbsentRoutesAbsent(t *testing.T) {
	h := HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "Script proposals")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/unraid/proposals/p1/approve", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// Approving persists the decision on the fake synchronously — by the time
// the handler responds, Proposal() already reflects it — before the JSON
// response (the only signal a polling MCP caller gets) is written.
func TestAPIProposalApprove_PersistsBeforeResponding(t *testing.T) {
	ps := newFakeProposalSource(Proposal{ID: "p1", Title: "restart plex", DryRunOutput: "would restart plex"})
	h := HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), ps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/unraid/proposals/p1/approve?who=noah", nil)
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "approved", got["decision"])
	require.Equal(t, "noah", got["decided_by"])

	// The fake's own state (what a real registry would durably persist)
	// already carries the decision — proving the response was written only
	// after the persist, not concurrently with or before it.
	require.Len(t, ps.decisions, 1)
	require.Equal(t, fakeDecision{kind: "approve", id: "p1", who: "noah"}, ps.decisions[0])
	stored, err := ps.Proposal(context.Background(), "p1")
	require.NoError(t, err)
	require.Equal(t, "approved", stored.Decision)
}

func TestAPIProposalDecision_UnknownIDIs404(t *testing.T) {
	ps := newFakeProposalSource()
	h := HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), ps)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/unraid/proposals/nope/approve", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPIProposalDecision_NonPendingIs409(t *testing.T) {
	ps := newFakeProposalSource(Proposal{ID: "p1", Decision: "approved"})
	h := HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), ps)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/unraid/proposals/p1/approve", nil))
	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestAPIProposalDecision_MethodNotAllowedOnGET(t *testing.T) {
	ps := newFakeProposalSource(Proposal{ID: "p1"})
	h := HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), ps)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/unraid/proposals/p1/approve", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// Denying mirrors approving for the second route sharing
// handleAPIProposalDecision.
func TestAPIProposalDeny_Success(t *testing.T) {
	ps := newFakeProposalSource(Proposal{ID: "p1"})
	h := HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), ps)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/unraid/proposals/p1/deny?who=noah", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "denied", got["decision"])
}

// Sanity check the whole page renders without htmx/self-refresh wiring
// breaking, and that no build step leaked in — vendored assets only.
func TestUnraidPage_UsesVendoredHTMXNoNewJS(t *testing.T) {
	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, fullFakeUnraidSource(), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	body := rec.Body.String()
	require.Contains(t, body, `src="/static/htmx.min.js"`)
	require.False(t, strings.Contains(body, "<script src=\"http"), "no externally-hosted script")
}

// Provenance must reach the human. When array state came from the emhttp INI
// fallback rather than unraid-api, the panel says so — otherwise a user
// cannot tell "this machine has no parity data" from "we read this the cheap
// way and that field isn't in this source" (CONVENTIONS rule 5). The full-API
// path must NOT show the notice, or it becomes noise everyone learns to skip.
func TestUnraidPage_EmhttpFallbackIsDisclosedApiPathIsNot(t *testing.T) {
	degraded := fullFakeUnraidSource()
	degraded.array.Source = core.SourceEmhttp

	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, degraded, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "not the Unraid API")

	full := fullFakeUnraidSource()
	full.array.Source = core.SourceUnraidAPI

	rec = httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, full, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "not the Unraid API")
}

// Host vitals read from the Host's bind-mounted procfs must say so. The
// numbers are real measurements, so the notice names the SOURCE and must not
// imply the values are estimates — and it must be absent on the full API
// path, or it degrades into noise users learn to ignore.
func TestUnraidPage_ProcfsHostVitalsAreDisclosedApiPathIsNot(t *testing.T) {
	degraded := fullFakeUnraidSource()
	degraded.host.Source = core.SourceProcfs

	rec := httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, degraded, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "not the Unraid API")
	require.Contains(t, body, "not estimates")

	full := fullFakeUnraidSource()
	full.host.Source = core.SourceUnraidAPI

	rec = httptest.NewRecorder()
	HandlerFull(&fakeVS{}, nil, full, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unraid", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "not estimates")
}

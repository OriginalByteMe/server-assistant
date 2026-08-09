package harness

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// --- fakes -------------------------------------------------------------

// fakeReasoner is a configurable core.Reasoner. reply/err/healthy are
// ordinarily set once before the first ObserveCommit (safe: that
// happens-before any concurrent access via the goroutine ObserveCommit
// launches); calls is mutated concurrently and always read through mu.
type fakeReasoner struct {
	mu      sync.Mutex
	reply   core.ReasonerReply
	err     error
	healthy error
	calls   int
}

var _ core.Reasoner = (*fakeReasoner)(nil)

func (f *fakeReasoner) Name() string { return "fake-reasoner" }

func (f *fakeReasoner) Diagnose(ctx context.Context, prompt string) (core.ReasonerReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.reply, f.err
}

func (f *fakeReasoner) Healthy(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healthy
}

func (f *fakeReasoner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeActuator is a configurable core.Actuator that records every dispatch.
type fakeActuator struct {
	mu       sync.Mutex
	err      error
	healthy  error
	restarts []string
}

var _ core.Actuator = (*fakeActuator)(nil)

func (f *fakeActuator) RestartContainer(ctx context.Context, container string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.restarts = append(f.restarts, container)
	return nil
}

func (f *fakeActuator) Healthy(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healthy
}

func (f *fakeActuator) restartsCopy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.restarts...)
}

// fakeTool is a configurable core.ReadTool that counts its calls.
type fakeTool struct {
	mu     sync.Mutex
	name   string
	output string
	err    error
	calls  int
}

var _ core.ReadTool = (*fakeTool)(nil)

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return "fake tool" }

func (f *fakeTool) Call(ctx context.Context, args map[string]string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.output, f.err
}

func (f *fakeTool) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// memStore is an in-memory core.Store. Only the harness cycle methods do
// anything; every other method is an unused no-op stub.
type memStore struct {
	mu       sync.Mutex
	cycles   map[string]core.HarnessCycle
	savedIDs []string // every ID SaveHarnessCycle was ever called with, in call order
	saveErr  error    // when set, SaveHarnessCycle fails without mutating state
}

func newMemStore() *memStore {
	return &memStore{cycles: make(map[string]core.HarnessCycle)}
}

var _ core.Store = (*memStore)(nil)

func (s *memStore) Migrate(ctx context.Context) error                         { return nil }
func (s *memStore) RecordProbe(ctx context.Context, p core.ProbeSample) error { return nil }
func (s *memStore) SaveCommittedStatus(ctx context.Context, cs core.CommittedStatus) error {
	return nil
}
func (s *memStore) LoadCommittedStatuses(ctx context.Context) ([]core.CommittedStatus, error) {
	return nil, nil
}
func (s *memStore) LoadProbeSamples(ctx context.Context, service string, limit int) ([]core.ProbeSample, error) {
	return nil, nil
}
func (s *memStore) PruneProbeSamples(ctx context.Context, service string, before time.Time) error {
	return nil
}
func (s *memStore) Close() error { return nil }

func (s *memStore) SaveHarnessCycle(ctx context.Context, c core.HarnessCycle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.cycles[c.ID] = c
	s.savedIDs = append(s.savedIDs, c.ID)
	return nil
}

// setSaveErr toggles SaveHarnessCycle's fault injection.
func (s *memStore) setSaveErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveErr = err
}

// wasSaved reports whether SaveHarnessCycle was ever called for id.
func (s *memStore) wasSaved(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, saved := range s.savedIDs {
		if saved == id {
			return true
		}
	}
	return false
}

// seed inserts a cycle directly, bypassing SaveHarnessCycle's savedIDs
// tracking, so Reconcile tests can distinguish "already there before
// Reconcile ran" from "Reconcile itself (re)saved it".
func (s *memStore) seed(c core.HarnessCycle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cycles[c.ID] = c
}

func (s *memStore) ListHarnessCycles(ctx context.Context, limit int) ([]core.HarnessCycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.HarnessCycle, 0, len(s.cycles))
	for _, c := range s.cycles {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memStore) GetHarnessCycle(ctx context.Context, id string) (core.HarnessCycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cycles[id]
	if !ok {
		return core.HarnessCycle{}, fmt.Errorf("harness cycle %q not found", id)
	}
	return c, nil
}

// latest returns the most recently started cycle, if any. Every scenario
// below runs at most one cycle at a time, so "most recent" is unambiguous.
func (s *memStore) latest() (core.HarnessCycle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best core.HarnessCycle
	found := false
	for _, c := range s.cycles {
		if !found || c.StartedAt.After(best.StartedAt) {
			best = c
			found = true
		}
	}
	return best, found
}

func (s *memStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cycles)
}

// --- test helpers --------------------------------------------------------

// waitFor polls store's latest cycle against pred rather than sleeping
// blindly, failing the test if timeout elapses first.
func waitFor(t *testing.T, store *memStore, timeout time.Duration, pred func(core.HarnessCycle) bool) core.HarnessCycle {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if c, ok := store.latest(); ok && pred(c) {
			return c
		}
		if time.Now().After(deadline) {
			c, _ := store.latest()
			t.Fatalf("timed out after %s waiting for cycle condition; last seen: %+v", timeout, c)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// requireStable polls cond across grace and fails the instant it goes
// false — asserts a negative ("nothing happened") without a single blind
// sleep-then-check, which could just as easily race past a delayed
// violation.
func requireStable(t *testing.T, grace time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		require.True(t, cond())
		time.Sleep(2 * time.Millisecond)
	}
}

func waitForNoCycle(t *testing.T, store *memStore, grace time.Duration) {
	t.Helper()
	requireStable(t, grace, func() bool { return store.count() == 0 })
}

// pendingWithProposal identifies "diagnosis complete, genuinely awaiting an
// Operator decision on a restart proposal". Approval alone is not enough to
// poll on: its zero value (ApprovalPending) is indistinguishable from "not
// yet processed".
func pendingWithProposal(c core.HarnessCycle) bool {
	return c.Approval == core.ApprovalPending && c.Diagnosis.Proposed.Kind == core.ActionRestartContainer
}

type harnessDeps struct {
	reasoner *fakeReasoner
	actuator *fakeActuator
	store    *memStore
}

// newTestHarness builds a Harness wired to fresh fakes with millisecond-
// scale timeouts so the suite stays fast. mode and overrides let each test
// tune only what it needs; the default Reasoner reply proposes restarting
// "web", which resolves to "sa-demo-web" in Targets.
func newTestHarness(t *testing.T, mode core.HarnessMode, overrides func(*Options)) (*Harness, harnessDeps) {
	t.Helper()
	reasoner := &fakeReasoner{
		reply: core.ReasonerReply{
			Action:    core.ActionRestartContainer,
			Subject:   "web",
			Rationale: "test rationale",
			Summary:   "test summary",
		},
	}
	actuator := &fakeActuator{}
	tool1 := &fakeTool{name: "tool1", output: "ok"}
	tool2 := &fakeTool{name: "tool2", output: "ok"}
	st := newMemStore()

	opts := Options{
		Mode:            mode,
		Store:           st,
		Reasoner:        reasoner,
		Actuator:        actuator,
		Tools:           []core.ReadTool{tool1, tool2},
		Targets:         map[string]string{"web": "sa-demo-web", "db": "sa-demo-db"},
		MaxToolCalls:    2,
		WallClock:       30 * time.Millisecond,
		ApprovalTimeout: 40 * time.Millisecond,
		Cooldown:        200 * time.Millisecond,
		OutcomeWindow:   40 * time.Millisecond,
	}
	if overrides != nil {
		overrides(&opts)
	}
	return New(opts), harnessDeps{reasoner: reasoner, actuator: actuator, store: st}
}

func down(service string) core.CommittedStatus {
	return core.CommittedStatus{Service: service, Status: core.StatusDown, ChangedAt: time.Now()}
}

func up(service string) core.CommittedStatus {
	return core.CommittedStatus{Service: service, Status: core.StatusUp, ChangedAt: time.Now()}
}

// --- tests -----------------------------------------------------------------

// TestHarness_OffModeNeverCallsReasoner: HarnessOff must not start a cycle
// or touch the Reasoner at all.
func TestHarness_OffModeNeverCallsReasoner(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessOff, nil)
	require.NoError(t, h.ObserveCommit(context.Background(), down("web")))
	waitForNoCycle(t, deps.store, 50*time.Millisecond)
	require.Equal(t, 0, deps.reasoner.callCount())
}

// TestHarness_UnknownTriggerStartsNothing: ADR 0005 — UNKNOWN must never be
// treated as a trigger.
func TestHarness_UnknownTriggerStartsNothing(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	cs := core.CommittedStatus{Service: "web", Status: core.StatusUnknown, ChangedAt: time.Now()}
	require.NoError(t, h.ObserveCommit(context.Background(), cs))
	waitForNoCycle(t, deps.store, 50*time.Millisecond)
}

// TestHarness_ShadowRecordsFullCycleNoDispatch: shadow mode still runs and
// audits a full Diagnosis but never asks for Approval and never dispatches
// (ADR 0014).
func TestHarness_ShadowRecordsFullCycleNoDispatch(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessShadow, nil)
	require.NoError(t, h.ObserveCommit(context.Background(), down("web")))

	cycle := waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		return c.Approval == core.ApprovalNotRequested
	})
	require.Equal(t, "web", cycle.Subject)
	require.Equal(t, core.StatusDown, cycle.TriggerStatus)
	require.Equal(t, core.HarnessShadow, cycle.Mode)
	require.Len(t, cycle.ToolCalls, 2)
	require.Equal(t, core.ActionRestartContainer, cycle.Diagnosis.Proposed.Kind)
	require.Equal(t, "sa-demo-web", cycle.ResolvedTarget)
	require.Equal(t, core.OutcomeNone, cycle.Outcome)
	require.Empty(t, deps.actuator.restartsCopy())
}

// TestHarness_LiveApproveDispatchesResolvedTargetAndRecovers: an Approve
// dispatches exactly once, using the harness-resolved container name (never
// the Service name), and a subsequent UP commit resolves the outcome to
// Recovered.
func TestHarness_LiveApproveDispatchesResolvedTargetAndRecovers(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()
	require.NoError(t, h.ObserveCommit(ctx, down("web")))

	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)
	require.NoError(t, h.Approve(ctx, pending.ID, "operator"))

	dispatched := waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		return c.DispatchResult == "dispatched"
	})
	require.Equal(t, core.ApprovalApproved, dispatched.Approval)
	require.Equal(t, "operator", dispatched.ApprovedBy)
	require.False(t, dispatched.ApprovedAt.IsZero())
	require.Equal(t, "sa-demo-web", dispatched.ResolvedTarget)
	require.Equal(t, core.OutcomePending, dispatched.Outcome)
	require.Equal(t, []string{"sa-demo-web"}, deps.actuator.restartsCopy())

	require.NoError(t, h.ObserveCommit(ctx, up("web")))

	recovered := waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		return c.Outcome == core.OutcomeRecovered
	})
	require.False(t, recovered.OutcomeAt.IsZero())
	require.Equal(t, []string{"sa-demo-web"}, deps.actuator.restartsCopy(), "exactly one dispatch, never a retry")
}

// TestHarness_LiveDenyDispatchesNothing: a Deny must never reach the
// Actuator.
func TestHarness_LiveDenyDispatchesNothing(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()
	require.NoError(t, h.ObserveCommit(ctx, down("web")))

	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)
	require.NoError(t, h.Deny(ctx, pending.ID, "operator"))

	final := waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		// decide() now persists Approval=Denied synchronously; runCycle
		// sets Outcome=OutcomeNone moments later in a separate persist —
		// wait for both, not just Approval, or this can observe the
		// window in between.
		return c.Approval == core.ApprovalDenied && c.Outcome == core.OutcomeNone
	})
	require.Equal(t, core.OutcomeNone, final.Outcome)
	require.Empty(t, deps.actuator.restartsCopy())
}

// TestHarness_ApprovalTimeoutExpiresAndDoesNotDispatch: an un-decided
// approval defaults to deny (ADR 0009) and never dispatches.
func TestHarness_ApprovalTimeoutExpiresAndDoesNotDispatch(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, func(o *Options) {
		o.ApprovalTimeout = 15 * time.Millisecond
	})
	require.NoError(t, h.ObserveCommit(context.Background(), down("web")))

	final := waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		return c.Approval == core.ApprovalExpired
	})
	require.Equal(t, core.OutcomeNone, final.Outcome)
	require.Empty(t, deps.actuator.restartsCopy())
}

// TestHarness_ReasonerErrorFallsBackToRunbookAndReachesApproval: a Reasoner
// failure never stalls or silently no-ops the cycle — the deterministic
// runbook still reaches Approval, flagged Fallback: true.
func TestHarness_ReasonerErrorFallsBackToRunbookAndReachesApproval(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	deps.reasoner.err = errors.New("backend unreachable")

	require.NoError(t, h.ObserveCommit(context.Background(), down("web")))

	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)
	require.True(t, pending.Diagnosis.Fallback)
	require.Equal(t, "runbook", pending.Diagnosis.Usage.Backend)
	require.Equal(t, "web", pending.Diagnosis.Proposed.Subject)
	require.Equal(t, "sa-demo-web", pending.ResolvedTarget)
	require.Contains(t, pending.Error, "backend unreachable")

	require.NoError(t, h.Deny(context.Background(), pending.ID, "operator")) // let the goroutine finish cleanly
}

// TestHarness_UnknownProposedSubjectDowngradesToNone: a Reasoner that names
// a Service outside Targets is never trusted — the proposal is downgraded
// to ActionNone (ADR 0018) and, since Live's "actionable proposal" rule no
// longer applies, Approval is skipped entirely, exactly like shadow/none.
func TestHarness_UnknownProposedSubjectDowngradesToNone(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	deps.reasoner.reply = core.ReasonerReply{
		Action:    core.ActionRestartContainer,
		Subject:   "does-not-exist",
		Rationale: "hallucinated",
		Summary:   "hallucinated",
	}
	require.NoError(t, h.ObserveCommit(context.Background(), down("web")))

	final := waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		return c.Approval == core.ApprovalNotRequested
	})
	require.Equal(t, core.ActionNone, final.Diagnosis.Proposed.Kind)
	require.Contains(t, final.Error, "does-not-exist")
	require.Equal(t, core.OutcomeNone, final.Outcome)
	require.Empty(t, deps.actuator.restartsCopy())
}

// TestHarness_SecondDownWhileInFlightIsDropped: single-flight is global
// (one cycleState field, not per-subject) — a different subject going DOWN
// while the first is in flight must be dropped too.
func TestHarness_SecondDownWhileInFlightIsDropped(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()
	require.NoError(t, h.ObserveCommit(ctx, down("web")))

	first := waitFor(t, deps.store, time.Second, pendingWithProposal)

	require.NoError(t, h.ObserveCommit(ctx, down("db")))
	requireStable(t, 20*time.Millisecond, func() bool {
		return deps.store.count() == 1 && deps.reasoner.callCount() == 1
	})

	require.NoError(t, h.Deny(ctx, first.ID, "operator"))
}

// TestHarness_CooldownSuppressesRepeat: a subject that just resolved must
// not immediately re-trigger.
func TestHarness_CooldownSuppressesRepeat(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, func(o *Options) {
		o.Cooldown = time.Second
	})
	ctx := context.Background()
	require.NoError(t, h.ObserveCommit(ctx, down("web")))

	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)
	require.NoError(t, h.Approve(ctx, pending.ID, "operator"))
	require.NoError(t, h.ObserveCommit(ctx, up("web")))
	waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		return c.Outcome == core.OutcomeRecovered
	})

	require.NoError(t, h.ObserveCommit(ctx, down("web")))
	requireStable(t, 20*time.Millisecond, func() bool { return deps.store.count() == 1 })
	require.Equal(t, 1, deps.reasoner.callCount())
}

// TestHarness_HaltSuppressesRearmRestores: Halt is sticky until Rearm.
func TestHarness_HaltSuppressesRearmRestores(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()

	h.Halt("maintenance")
	require.True(t, h.Halted())
	require.NoError(t, h.ObserveCommit(ctx, down("web")))
	waitForNoCycle(t, deps.store, 30*time.Millisecond)

	h.Rearm()
	require.False(t, h.Halted())
	require.NoError(t, h.ObserveCommit(ctx, down("web")))
	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)
	require.NoError(t, h.Deny(ctx, pending.ID, "operator"))
}

// TestHarness_OutcomeWindowExpiryYieldsActionFailed: no recovering UP
// within OutcomeWindow means the action is adjudicated failed.
func TestHarness_OutcomeWindowExpiryYieldsActionFailed(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, func(o *Options) {
		o.OutcomeWindow = 15 * time.Millisecond
	})
	ctx := context.Background()
	require.NoError(t, h.ObserveCommit(ctx, down("web")))

	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)
	require.NoError(t, h.Approve(ctx, pending.ID, "operator"))

	final := waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		return c.Outcome == core.OutcomeActionFailed
	})
	require.False(t, final.OutcomeAt.IsZero())
}

// TestHarness_UpRacingApprovalStillRecovers: a fast-recovering container
// can commit UP before runCycle finishes dispatch bookkeeping and starts
// listening on outcomeCh — the edge-triggered signal alone would then be
// dropped on the floor and the incident would be permanently
// mis-adjudicated as action_failed even though the subject genuinely
// recovered. The outcome watch must be level-triggered (ADR 0016): a
// recovery that already happened must never be lost to this timing race.
func TestHarness_UpRacingApprovalStillRecovers(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, func(o *Options) {
		o.OutcomeWindow = 15 * time.Millisecond
	})
	ctx := context.Background()
	require.NoError(t, h.ObserveCommit(ctx, down("web")))

	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)
	require.NoError(t, h.Approve(ctx, pending.ID, "operator"))
	// Deliver the recovery immediately after Approve returns — before
	// runCycle has necessarily finished dispatch bookkeeping and started
	// listening on outcomeCh.
	require.NoError(t, h.ObserveCommit(ctx, up("web")))

	recovered := waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		return c.Outcome == core.OutcomeRecovered
	})
	require.False(t, recovered.OutcomeAt.IsZero())
}

// TestHarness_MaxToolCallsRespected: the sweep stops after MaxToolCalls,
// never touching the remaining configured tools.
func TestHarness_MaxToolCallsRespected(t *testing.T) {
	tool1 := &fakeTool{name: "t1", output: "a"}
	tool2 := &fakeTool{name: "t2", output: "b"}
	tool3 := &fakeTool{name: "t3", output: "c"}
	h, deps := newTestHarness(t, core.HarnessShadow, func(o *Options) {
		o.Tools = []core.ReadTool{tool1, tool2, tool3}
		o.MaxToolCalls = 2
	})
	require.NoError(t, h.ObserveCommit(context.Background(), down("web")))

	cycle := waitFor(t, deps.store, time.Second, func(c core.HarnessCycle) bool {
		return c.Approval == core.ApprovalNotRequested
	})
	require.Len(t, cycle.ToolCalls, 2)
	require.Equal(t, "t1", cycle.ToolCalls[0].Tool)
	require.Equal(t, "t2", cycle.ToolCalls[1].Tool)
	require.Equal(t, 1, tool1.callCount())
	require.Equal(t, 1, tool2.callCount())
	require.Equal(t, 0, tool3.callCount())
}

// --- Reconcile ---------------------------------------------------------

// TestHarness_ReconcilePendingApprovalBecomesExpired: a cycle orphaned
// while awaiting Approval (its owning process died mid-flight) is
// force-resolved to the only safe default-deny outcome (ADR 0009), and
// Outcome mirrors the normal "not approved" path since nothing was ever
// dispatched.
func TestHarness_ReconcilePendingApprovalBecomesExpired(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()

	deps.store.seed(core.HarnessCycle{
		ID:            "orphan-pending",
		Subject:       "web",
		TriggerStatus: core.StatusDown,
		Mode:          core.HarnessLive,
		StartedAt:     time.Now().Add(-time.Hour),
		Diagnosis: core.Diagnosis{
			Proposed: core.ProposedAction{Kind: core.ActionRestartContainer, Subject: "web"},
		},
		Approval:       core.ApprovalPending,
		ResolvedTarget: "sa-demo-web",
	})

	require.NoError(t, h.Reconcile(ctx))

	got, err := deps.store.GetHarnessCycle(ctx, "orphan-pending")
	require.NoError(t, err)
	require.Equal(t, core.ApprovalExpired, got.Approval)
	require.Equal(t, core.OutcomeNone, got.Outcome)
	require.Contains(t, got.Error, "interrupted by a process restart")
	require.True(t, deps.store.wasSaved("orphan-pending"))
}

// TestHarness_ReconcileDispatchedOutcomePendingBecomesActionFailed: a cycle
// orphaned after dispatch, mid outcome-wait, must never be recorded as
// recovered — the recovery window can no longer be observed (ADR 0016).
func TestHarness_ReconcileDispatchedOutcomePendingBecomesActionFailed(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()

	deps.store.seed(core.HarnessCycle{
		ID:             "orphan-dispatched",
		Subject:        "web",
		TriggerStatus:  core.StatusDown,
		Mode:           core.HarnessLive,
		StartedAt:      time.Now().Add(-time.Hour),
		Approval:       core.ApprovalApproved,
		ApprovedBy:     "operator",
		ApprovedAt:     time.Now().Add(-50 * time.Minute),
		ResolvedTarget: "sa-demo-web",
		DispatchResult: "dispatched",
		DispatchedAt:   time.Now().Add(-49 * time.Minute),
		Outcome:        core.OutcomePending,
	})

	require.NoError(t, h.Reconcile(ctx))

	got, err := deps.store.GetHarnessCycle(ctx, "orphan-dispatched")
	require.NoError(t, err)
	require.Equal(t, core.ApprovalApproved, got.Approval, "already-terminal Approval must not change")
	require.Equal(t, core.OutcomeActionFailed, got.Outcome)
	require.Contains(t, got.Error, "interrupted by a process restart")
	require.True(t, deps.store.wasSaved("orphan-dispatched"))
}

// TestHarness_ReconcileLeavesTerminalCycleUnchanged: a fully resolved cycle
// is left byte-identical and is never even re-saved.
func TestHarness_ReconcileLeavesTerminalCycleUnchanged(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()

	at := time.Now().Add(-time.Hour)
	seed := core.HarnessCycle{
		ID:             "terminal-recovered",
		Subject:        "web",
		TriggerStatus:  core.StatusDown,
		Mode:           core.HarnessLive,
		StartedAt:      at,
		Approval:       core.ApprovalApproved,
		ApprovedBy:     "operator",
		ApprovedAt:     at.Add(time.Second),
		ResolvedTarget: "sa-demo-web",
		DispatchResult: "dispatched",
		DispatchedAt:   at.Add(2 * time.Second),
		Outcome:        core.OutcomeRecovered,
		OutcomeAt:      at.Add(3 * time.Second),
	}
	deps.store.seed(seed)

	require.NoError(t, h.Reconcile(ctx))

	got, err := deps.store.GetHarnessCycle(ctx, "terminal-recovered")
	require.NoError(t, err)
	require.Equal(t, seed, got, "a terminal cycle must be left byte-identical")
	require.False(t, deps.store.wasSaved("terminal-recovered"), "a terminal cycle must never be re-saved")
}

// TestHarness_ReconcileEmptyStoreIsNoop: Reconcile against an empty Store
// is a no-op returning nil.
func TestHarness_ReconcileEmptyStoreIsNoop(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	require.NoError(t, h.Reconcile(context.Background()))
	require.Equal(t, 0, deps.store.count())
	require.Empty(t, deps.store.savedIDs)
}

// --- Approve/Deny durability ---------------------------------------------

// TestHarness_ApproveDurablyPersistsBeforeReturning: ADR 0019 makes who
// decided and when part of the durable record — the Store must already
// show the decision the instant Approve returns, not eventually once the
// cycle goroutine gets around to it.
func TestHarness_ApproveDurablyPersistsBeforeReturning(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()
	require.NoError(t, h.ObserveCommit(ctx, down("web")))

	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)
	require.NoError(t, h.Approve(ctx, pending.ID, "operator"))

	// No waitFor/poll here on purpose: this must already be true the
	// instant Approve returns, before any wait for dispatch.
	got, err := deps.store.GetHarnessCycle(ctx, pending.ID)
	require.NoError(t, err)
	require.Equal(t, core.ApprovalApproved, got.Approval)
	require.Equal(t, "operator", got.ApprovedBy)
	require.False(t, got.ApprovedAt.IsZero())
}

// TestHarness_DenyDurablyPersistsBeforeReturning: same durability
// guarantee for Deny.
func TestHarness_DenyDurablyPersistsBeforeReturning(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()
	require.NoError(t, h.ObserveCommit(ctx, down("web")))

	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)
	require.NoError(t, h.Deny(ctx, pending.ID, "operator"))

	got, err := deps.store.GetHarnessCycle(ctx, pending.ID)
	require.NoError(t, err)
	require.Equal(t, core.ApprovalDenied, got.Approval)
	require.Equal(t, "operator", got.ApprovedBy)
	require.False(t, got.ApprovedAt.IsZero())
}

// TestHarness_ApproveReturnsErrorWhenStoreSaveFails: a decision that could
// not be durably recorded must not take effect — fail-closed. The cycle
// stays Pending and nothing dispatches.
func TestHarness_ApproveReturnsErrorWhenStoreSaveFails(t *testing.T) {
	h, deps := newTestHarness(t, core.HarnessLive, nil)
	ctx := context.Background()
	require.NoError(t, h.ObserveCommit(ctx, down("web")))

	pending := waitFor(t, deps.store, time.Second, pendingWithProposal)

	deps.store.setSaveErr(errors.New("disk full"))
	err := h.Approve(ctx, pending.ID, "operator")
	require.Error(t, err)
	require.Contains(t, err.Error(), "disk full")

	got, gerr := deps.store.GetHarnessCycle(ctx, pending.ID)
	require.NoError(t, gerr)
	require.Equal(t, core.ApprovalPending, got.Approval, "a decision that failed to persist must not take effect")
	require.Empty(t, deps.actuator.restartsCopy())

	// Clear the fault and let a retry through so the cycle resolves
	// cleanly instead of lingering past the test until ApprovalTimeout.
	deps.store.setSaveErr(nil)
	require.NoError(t, h.Deny(ctx, pending.ID, "operator"))
}

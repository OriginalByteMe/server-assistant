// Package harness implements the M2 bounded management agent (ADR 0009):
// read-only agentic Diagnosis -> at most one typed Action -> explicit
// Operator Approval (live mode only) -> scoped Actuator dispatch -> outcome
// adjudicated solely by the v1 monitoring spine's next committed Status
// (ADR 0016) — the Actuator never grades its own homework. Shadow mode runs
// the identical Diagnosis but never proposes to a human and never
// dispatches (ADR 0014); every cycle, shadow or live, is persisted as an
// append-only accountability record (ADR 0019). The engine — never the
// Reasoner — resolves a Diagnosis's Service subject to an implementation
// container (ADR 0018).
package harness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"server-assistant/internal/core"
	"server-assistant/internal/monitor"
)

// cycleSlack bounds how much longer a cycle's derived context outlives the
// sum of its own stage timeouts (WallClock + ApprovalTimeout +
// OutcomeWindow). It is a circuit breaker against a hung Store/Actuator
// call, never a timer any normal path waits on.
const cycleSlack = 5 * time.Second

// reconcileListLimit bounds Reconcile's Store.ListHarnessCycles call. A
// fixed bound, not 0/negative (the Store's LIMIT is a literal SQL LIMIT — 0
// or negative returns nothing, not everything). 1000 is far more than
// Reconcile actually needs: an interrupted cycle is always among the
// newest rows, since the process that owned it cannot have written any
// newer cycle after it died.
const reconcileListLimit = 1000

// Options configures a Harness. Every field is immutable after New.
type Options struct {
	Mode            core.HarnessMode
	Store           core.Store
	Reasoner        core.Reasoner
	Actuator        core.Actuator
	Tools           []core.ReadTool
	Targets         map[string]string // Service -> container (ADR 0018 resolution)
	MaxToolCalls    int
	WallClock       time.Duration
	ApprovalTimeout time.Duration
	Cooldown        time.Duration
	OutcomeWindow   time.Duration
	Logger          *slog.Logger
}

// approvalMsg is what Approve/Deny hands to the runCycle goroutine waiting
// on an Operator decision.
type approvalMsg struct {
	decision core.ApprovalDecision
	who      string
	at       time.Time
}

// cycleState is the mutable, in-flight half of one incident; the durable
// half is the core.HarnessCycle persisted to the Store. Every field is
// guarded by Harness.mu.
type cycleState struct {
	id      string
	subject string

	// cycle is the last known cycle content, snapshotted under h.mu when
	// awaitingApproval becomes true. decide() (Approve/Deny) reads and
	// mutates this directly to persist the Approval decision itself,
	// synchronously, before signalling runCycle — it is otherwise only
	// written by runCycle, and only while it is not blocked waiting on
	// decisionCh, so there is never a concurrent writer.
	cycle core.HarnessCycle

	// decisionCh carries the Operator's decision to the waiting runCycle
	// goroutine. Buffered 1 so Approve/Deny never blocks.
	decisionCh chan approvalMsg
	// outcomeCh is signalled by ObserveCommit when the subject reports UP
	// while this cycle awaits outcome. Buffered 1 for the same reason.
	outcomeCh chan struct{}

	awaitingApproval bool
	awaitingOutcome  bool
}

// Harness is the M2 engine: created once at startup, safe for concurrent
// use. ObserveCommit is invoked from the v1 spine's per-Service poll
// goroutines (monitor.CommitSink); Approve/Deny/Halt/Rearm are invoked from
// the dashboard's HTTP handlers.
type Harness struct {
	opts Options
	log  *slog.Logger

	mu         sync.Mutex
	halted     bool
	haltReason string
	active     *cycleState // nil = idle; also the global single-flight gate (ADR 0009: at most one Diagnosis in flight)
	cooldown   map[string]time.Time
	lastUp     map[string]time.Time // per-subject last observed UP (ADR 0016 level-triggered outcome check)
}

// New builds a Harness from o. o.Logger may be nil.
func New(o Options) *Harness {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Harness{
		opts:     o,
		log:      log,
		cooldown: make(map[string]time.Time),
		lastUp:   make(map[string]time.Time),
	}
}

// Reconcile force-resolves every non-terminal cycle left behind by a
// process that died or was restarted while a cycle was in flight — the
// goroutine that owned its Approval or outcome timeout died with the
// process, so nothing will ever move it out of Pending on its own
// (ADR 0019: the audit trail must durably answer what the harness did,
// never claim an incident is still awaiting an Operator who can no longer
// act on it). A cycle already in a terminal state is left untouched — not
// even re-saved.
//
// Reconcile MUST be called once at startup, before the Monitor starts
// feeding ObserveCommit: it does not coordinate with h.mu/h.active at all,
// relying on there being no in-flight cycle goroutine yet.
func (h *Harness) Reconcile(ctx context.Context) error {
	cycles, err := h.opts.Store.ListHarnessCycles(ctx, reconcileListLimit)
	if err != nil {
		return fmt.Errorf("reconcile: list harness cycles: %w", err)
	}

	for _, cycle := range cycles {
		changed := false
		if cycle.Approval == core.ApprovalPending {
			// Default-deny is the only safe resolution for an approval
			// nobody could ever answer (ADR 0009). Nothing was dispatched,
			// so Outcome mirrors the normal "not approved" path.
			cycle.Approval = core.ApprovalExpired
			cycle.Outcome = core.OutcomeNone
			changed = true
		}
		if cycle.Outcome == core.OutcomePending {
			// The action was dispatched and the recovery window can no
			// longer be observed; ADR 0016 forbids assuming success, so
			// this must never be recorded as recovered.
			cycle.Outcome = core.OutcomeActionFailed
			changed = true
		}
		if !changed {
			continue
		}

		cycle.Error = appendNote(cycle.Error, "interrupted by a process restart; resolved by startup reconciliation")
		if err := h.opts.Store.SaveHarnessCycle(ctx, cycle); err != nil {
			return fmt.Errorf("reconcile: save cycle %s: %w", cycle.ID, err)
		}
		h.log.Info("reconciled orphaned cycle", "id", cycle.ID, "subject", cycle.Subject, "approval", cycle.Approval.String(), "outcome", cycle.Outcome)
	}
	return nil
}

// Sink adapts ObserveCommit to monitor.CommitSink for wiring into the v1
// spine.
func (h *Harness) Sink() monitor.CommitSink {
	return h.ObserveCommit
}

// ObserveCommit is the v1 spine's post-commit handoff. It never blocks the
// caller: every branch is either a lock-guarded map/pointer check or a
// non-blocking channel send, and a triggered cycle runs on its own
// goroutine.
func (h *Harness) ObserveCommit(ctx context.Context, cs core.CommittedStatus) error {
	h.mu.Lock()

	// Level-triggered outcome tracking (ADR 0016): record every committed
	// UP unconditionally, independent of whether a cycle is even watching
	// yet. Edge-triggered signalling alone is lossy — a fast-recovering
	// container can commit UP before runCycle finishes dispatch
	// bookkeeping and starts listening, silently mis-adjudicating a real
	// recovery as action_failed. The outcome phase below checks this map
	// before blocking, so a recovery that already happened is never lost;
	// the v1 spine's committed Status is still the sole adjudicator, we
	// just stop discarding its verdict on a timing race.
	if cs.Status == core.StatusUp {
		h.lastUp[cs.Service] = cs.ChangedAt
	}

	// Outcome watch (fast path): signal an in-flight cycle immediately if
	// it is already listening. This is how an in-flight cycle resolves
	// even though the single-flight gate below blocks a *new* cycle from
	// starting for this same commit.
	if a := h.active; a != nil && a.subject == cs.Service && a.awaitingOutcome && cs.Status == core.StatusUp {
		select {
		case a.outcomeCh <- struct{}{}:
		default:
		}
	}

	reject := func(reason string) error {
		h.log.Debug("commit ignored", "service", cs.Service, "status", cs.Status.String(), "reason", reason)
		h.mu.Unlock()
		return nil
	}

	if h.opts.Mode == core.HarnessOff {
		return reject("mode off")
	}
	if h.halted {
		return reject("halted: " + h.haltReason)
	}
	if cs.Status != core.StatusDown {
		return reject("not a DOWN trigger")
	}
	if _, ok := h.opts.Targets[cs.Service]; !ok {
		return reject("no target mapping")
	}
	if h.active != nil {
		return reject("cycle already active")
	}
	if until, ok := h.cooldown[cs.Service]; ok && time.Now().Before(until) {
		return reject("in cooldown")
	}

	state := &cycleState{
		id:         core.NewCycleID(),
		subject:    cs.Service,
		decisionCh: make(chan approvalMsg, 1),
		outcomeCh:  make(chan struct{}, 1),
	}
	h.active = state
	h.mu.Unlock()

	startedAt := time.Now()
	total := h.opts.WallClock + h.opts.ApprovalTimeout + h.opts.OutcomeWindow + cycleSlack
	cycleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), total)
	go func() {
		defer cancel()
		h.runCycle(cycleCtx, state, cs, startedAt)
	}()
	return nil
}

// runCycle drives one incident end to end: bounded Diagnosis, Operator
// Approval (live mode only), Actuator dispatch, and outcome adjudication.
// It always clears h.active on return — even on an early return — so a new
// trigger can start (ADR 0009: at most one Diagnosis in flight).
func (h *Harness) runCycle(ctx context.Context, state *cycleState, cs core.CommittedStatus, startedAt time.Time) {
	defer func() {
		h.mu.Lock()
		h.active = nil
		h.mu.Unlock()
	}()

	cycle := core.HarnessCycle{
		ID:            state.id,
		Subject:       cs.Service,
		TriggerStatus: cs.Status,
		Mode:          h.opts.Mode,
		StartedAt:     startedAt,
	}
	h.persist(ctx, &cycle)
	h.log.Info("cycle started", "id", cycle.ID, "subject", cycle.Subject, "mode", h.opts.Mode.String())

	diagCtx, cancel := context.WithTimeout(ctx, h.opts.WallClock)
	cycle.ToolCalls = h.diagnosisSweep(diagCtx, cycle.Subject)
	cancel()
	h.persist(ctx, &cycle)

	h.diagnose(ctx, &cycle)
	h.validateProposal(&cycle)

	if h.opts.Mode == core.HarnessShadow || cycle.Diagnosis.Proposed.Kind == core.ActionNone {
		// Shadow is audited but never acts (ADR 0014); a Diagnosis that
		// proposed nothing has nothing for an Operator to decide either,
		// regardless of Mode.
		cycle.Approval = core.ApprovalNotRequested
		cycle.Outcome = core.OutcomeNone
		h.persist(ctx, &cycle)
		h.log.Info("cycle closed without dispatch", "id", cycle.ID, "mode", h.opts.Mode.String(), "proposed", cycle.Diagnosis.Proposed.Kind)
		return
	}

	// Live mode, actionable proposal: flip awaitingApproval BEFORE
	// persisting the cycle as Pending, so a caller that observes Pending
	// via the Store can call Approve/Deny immediately without racing this
	// goroutine's own bookkeeping — both sides serialize through h.mu, and
	// the Store write below happens-after the flag flip.
	h.mu.Lock()
	state.awaitingApproval = true
	cycle.Approval = core.ApprovalPending
	state.cycle = cycle // decide() needs this exact snapshot to persist its own decision
	h.mu.Unlock()
	h.persist(ctx, &cycle)

	var decision approvalMsg
	select {
	case decision = <-state.decisionCh:
	case <-time.After(h.opts.ApprovalTimeout):
		h.mu.Lock()
		state.awaitingApproval = false
		h.mu.Unlock()
		decision = approvalMsg{decision: core.ApprovalExpired} // default-deny (ADR 0009)
	}

	cycle.Approval = decision.decision
	cycle.ApprovedBy = decision.who
	if !decision.at.IsZero() {
		cycle.ApprovedAt = decision.at
	}
	if decision.decision != core.ApprovalApproved {
		cycle.Outcome = core.OutcomeNone
		h.persist(ctx, &cycle)
		h.log.Info("cycle closed without dispatch", "id", cycle.ID, "approval", decision.decision.String())
		return
	}

	h.dispatch(ctx, &cycle)
	if cycle.Outcome != core.OutcomePending {
		h.persist(ctx, &cycle)
		h.log.Info("dispatch failed", "id", cycle.ID, "result", cycle.DispatchResult)
		return
	}

	// Same ordering fix as approval: mark awaitingOutcome BEFORE persisting
	// the dispatched/Pending state.
	h.mu.Lock()
	state.awaitingOutcome = true
	// Level-triggered fast path (ADR 0016): the subject may already have
	// reported UP before this goroutine got here — e.g. Approve raced a
	// fast container restart. Anchor on ApprovedAt, not DispatchedAt, so a
	// recovery that races the dispatch bookkeeping itself still counts.
	upAt, hadUp := h.lastUp[cycle.Subject]
	alreadyRecovered := hadUp && !upAt.Before(cycle.ApprovedAt)
	h.mu.Unlock()
	h.persist(ctx, &cycle)

	if alreadyRecovered {
		cycle.Outcome = core.OutcomeRecovered
	} else {
		select {
		case <-state.outcomeCh:
			cycle.Outcome = core.OutcomeRecovered
		case <-time.After(h.opts.OutcomeWindow):
			cycle.Outcome = core.OutcomeActionFailed
		}
	}
	h.mu.Lock()
	state.awaitingOutcome = false
	delete(h.lastUp, cycle.Subject) // prune: this cycle has consumed or timed past it
	h.mu.Unlock()
	cycle.OutcomeAt = time.Now()
	h.persist(ctx, &cycle)
	h.log.Info("cycle resolved", "id", cycle.ID, "outcome", cycle.Outcome)

	h.mu.Lock()
	h.cooldown[cycle.Subject] = time.Now().Add(h.opts.Cooldown)
	h.mu.Unlock()
}

// diagnosisSweep calls up to MaxToolCalls of the configured tools, in
// order, bounded by ctx's deadline (WallClock). The Reasoner never picks
// tools and never sees a shell — this is the entire read-only
// evidence-gathering step.
func (h *Harness) diagnosisSweep(ctx context.Context, subject string) []core.ToolCall {
	n := len(h.opts.Tools)
	if h.opts.MaxToolCalls >= 0 && h.opts.MaxToolCalls < n {
		n = h.opts.MaxToolCalls
	}
	tools := h.opts.Tools[:n]

	args := map[string]string{"service": subject}
	calls := make([]core.ToolCall, 0, len(tools))
	for _, tool := range tools {
		if ctx.Err() != nil {
			break
		}
		start := time.Now()
		out, err := tool.Call(ctx, args)
		call := core.ToolCall{
			Tool:     tool.Name(),
			Args:     args,
			Output:   out,
			At:       start,
			Duration: time.Since(start),
		}
		if err != nil {
			call.Err = err.Error()
		}
		calls = append(calls, call)
	}
	return calls
}

// diagnose calls the Reasoner with a deterministic prompt built from the
// tool sweep. Any error — unreachable, timeout, garbage reply — is recorded
// on cycle.Error and replaced with the deterministic runbook fallback
// (ADR 0009 fail-closed): the harness always has something to show the
// Operator, never a silent no-op caused by backend flakiness.
func (h *Harness) diagnose(ctx context.Context, cycle *core.HarnessCycle) {
	services := make([]string, 0, len(h.opts.Targets))
	for svc := range h.opts.Targets {
		services = append(services, svc)
	}
	prompt := buildPrompt(cycle.TriggerStatus, cycle.Subject, cycle.ToolCalls, services)

	reply, err := h.opts.Reasoner.Diagnose(ctx, prompt)
	if err != nil {
		cycle.Error = fmt.Sprintf("reasoner: %v", err)
		cycle.Diagnosis = runbook(cycle.Subject, cycle.TriggerStatus)
		return
	}
	cycle.Diagnosis = core.Diagnosis{
		Summary: reply.Summary,
		Proposed: core.ProposedAction{
			Kind:      reply.Action,
			Subject:   reply.Subject,
			Rationale: reply.Rationale,
		},
		Usage: reply.Usage,
	}
}

// validateProposal enforces the Action catalog and the Service allowlist on
// whatever the Reasoner (or the runbook fallback) proposed, then resolves
// the Service to its implementation container — a resolution the Reasoner
// itself must never perform (ADR 0018). An invalid Kind or an unknown
// Subject is downgraded to ActionNone with a note appended to cycle.Error
// rather than trusted.
func (h *Harness) validateProposal(cycle *core.HarnessCycle) {
	p := cycle.Diagnosis.Proposed
	if p.Kind != core.ActionRestartContainer && p.Kind != core.ActionNone {
		cycle.Error = appendNote(cycle.Error, fmt.Sprintf("proposed unknown action %q, downgraded to none", p.Kind))
		cycle.Diagnosis.Proposed = core.ProposedAction{Kind: core.ActionNone}
		return
	}
	if p.Kind == core.ActionNone {
		return
	}
	target, ok := h.opts.Targets[p.Subject]
	if !ok {
		cycle.Error = appendNote(cycle.Error, fmt.Sprintf("proposed unknown subject %q, downgraded to none", p.Subject))
		cycle.Diagnosis.Proposed = core.ProposedAction{Kind: core.ActionNone}
		return
	}
	cycle.ResolvedTarget = target
}

func appendNote(existing, note string) string {
	if existing == "" {
		return note
	}
	return existing + "; " + note
}

// dispatch fires the Actuator exactly once (ADR 0016: never retry) and
// records the result. Only ever called after an explicit Approve.
func (h *Harness) dispatch(ctx context.Context, cycle *core.HarnessCycle) {
	if err := h.opts.Actuator.RestartContainer(ctx, cycle.ResolvedTarget); err != nil {
		cycle.DispatchResult = err.Error()
		cycle.Outcome = core.OutcomeDispatchErr
		return
	}
	cycle.DispatchResult = "dispatched"
	cycle.DispatchedAt = time.Now()
	cycle.Outcome = core.OutcomePending
}

// persist saves the cycle's current state so the dashboard can watch it
// progress. A Store failure is logged, never fatal to the running cycle
// (ADR 0009: M2 failure degrades, it does not take down monitoring).
func (h *Harness) persist(ctx context.Context, cycle *core.HarnessCycle) {
	if err := h.opts.Store.SaveHarnessCycle(ctx, *cycle); err != nil {
		h.log.Error("persist harness cycle failed", "id", cycle.ID, "err", err)
	}
}

// Approve and Deny are the Operator gate (ADR 0009). Both persist the
// terminal Approval decision themselves, synchronously, before returning:
// who decided and when is part of the durable accountability record
// (ADR 0019), and a caller must never observe success before the decision
// is actually recorded. If the Store write fails, the error is returned,
// the decision does NOT take effect (the cycle is left Pending and the
// waiting goroutine is not signalled — fail-closed, retriable), and
// nothing is dispatched. Both return an error when id names no in-flight
// cycle, or the cycle is no longer awaiting a decision (already decided,
// or its approval window already expired).
func (h *Harness) Approve(ctx context.Context, id, who string) error {
	return h.decide(ctx, id, who, core.ApprovalApproved)
}

func (h *Harness) Deny(ctx context.Context, id, who string) error {
	return h.decide(ctx, id, who, core.ApprovalDenied)
}

// decide holds h.mu across the Store write below, deliberately: it is the
// simplest way to make the persist-then-signal sequence atomic with the
// awaitingApproval check. This is safe today because every core.Store
// implementation (the sqlite Store and every test fake) is a leaf writer
// that never calls back into the Harness. If a Store implementation is
// ever wired that re-enters the Harness from within a write, this
// deadlocks — do not introduce one without revisiting this lock scope.
func (h *Harness) decide(ctx context.Context, id, who string, decision core.ApprovalDecision) error {
	h.mu.Lock()
	state := h.active
	if state == nil || state.id != id {
		h.mu.Unlock()
		return errors.New("unknown cycle")
	}
	if !state.awaitingApproval {
		h.mu.Unlock()
		return fmt.Errorf("cycle %s: not awaiting approval", id)
	}

	now := time.Now()
	cycle := state.cycle
	cycle.Approval = decision
	cycle.ApprovedBy = who
	cycle.ApprovedAt = now

	if err := h.opts.Store.SaveHarnessCycle(ctx, cycle); err != nil {
		h.mu.Unlock()
		return fmt.Errorf("persist approval decision: %w", err)
	}

	state.awaitingApproval = false
	state.cycle = cycle
	h.mu.Unlock()

	select {
	case state.decisionCh <- approvalMsg{decision: decision, who: who, at: now}:
	default:
		// runCycle already gave up (the approval timeout raced us);
		// nothing to deliver.
	}
	return nil
}

// Mode reports the configured HarnessMode. It never changes at runtime.
func (h *Harness) Mode() core.HarnessMode { return h.opts.Mode }

// Halted reports whether the harness is currently sticky-halted.
func (h *Harness) Halted() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.halted
}

// Halt sticks the harness off until Rearm, regardless of configured Mode.
// It does not cancel a cycle already in flight.
func (h *Harness) Halt(reason string) {
	h.mu.Lock()
	h.halted = true
	h.haltReason = reason
	h.mu.Unlock()
	h.log.Warn("harness halted", "reason", reason)
}

// Rearm clears a Halt.
func (h *Harness) Rearm() {
	h.mu.Lock()
	h.halted = false
	h.haltReason = ""
	h.mu.Unlock()
	h.log.Info("harness rearmed")
}

// Incidents and Incident delegate to the Store, the durable append-only
// accountability record (ADR 0019).
func (h *Harness) Incidents(ctx context.Context, limit int) ([]core.HarnessCycle, error) {
	return h.opts.Store.ListHarnessCycles(ctx, limit)
}

func (h *Harness) Incident(ctx context.Context, id string) (core.HarnessCycle, error) {
	return h.opts.Store.GetHarnessCycle(ctx, id)
}

// Healthy reports whether the harness's own dependencies are usable, for
// self-monitoring (ADR 0015: the harness is a subject like any other). The
// Actuator is only checked in live mode — shadow mode never dispatches, so
// an unreachable write credential is not a harness failure there.
func (h *Harness) Healthy(ctx context.Context) error {
	if err := h.opts.Reasoner.Healthy(ctx); err != nil {
		return fmt.Errorf("reasoner: %w", err)
	}
	if h.opts.Mode == core.HarnessLive {
		if err := h.opts.Actuator.Healthy(ctx); err != nil {
			return fmt.Errorf("actuator: %w", err)
		}
	}
	return nil
}

// Prober exposes the harness itself as a core.Prober (ADR 0015) so it is
// monitored the same way as every other subject.
func (h *Harness) Prober() core.Prober {
	return harnessProber{h: h}
}

type harnessProber struct{ h *Harness }

func (p harnessProber) Name() string { return "harness" }

// Probe maps a nil Healthy result to StatusUp and any error to StatusDown
// (ADR 0015). Down is a legitimate measurement, not an aborted Probe, so
// the second return value stays nil — mirroring internal/prober's
// convention (e.g. TCP.Probe): a confirmed-down measurement is not an error
// the monitor should skip.
func (p harnessProber) Probe(ctx context.Context) (core.ProbeResult, error) {
	start := time.Now()
	err := p.h.Healthy(ctx)
	latency := time.Since(start)
	if err != nil {
		return core.ProbeResult{Status: core.StatusDown, Latency: latency, Err: err}, nil
	}
	return core.ProbeResult{Status: core.StatusUp, Latency: latency}, nil
}

package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// M2 Harness domain and seams. Bounded management agent (ADR 0009): read-only
// agentic Diagnosis -> at most one typed Action -> explicit Operator Approval
// -> scoped Actuator. No LLM-authored code execution ever (ADR 0012); the LLM
// reasons in domain terms only and never names an implementation target
// (ADR 0018).

// HarnessMode is the enablement ramp (ADR 0014). Zero value is Off: the
// harness never runs unless explicitly enabled.
type HarnessMode int

const (
	HarnessOff    HarnessMode = iota // default; no Diagnosis at all
	HarnessShadow                    // full read-only Diagnosis, audited, no Approval, no mutation
	HarnessLive                      // Diagnosis -> Approval -> at most one Action
)

func (m HarnessMode) String() string {
	switch m {
	case HarnessShadow:
		return "shadow"
	case HarnessLive:
		return "live"
	default:
		return "off"
	}
}

// ParseHarnessMode maps config text to a mode. Unknown text is an error, never
// a silent fallback to a more permissive mode.
func ParseHarnessMode(s string) (HarnessMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "disabled":
		return HarnessOff, nil
	case "shadow":
		return HarnessShadow, nil
	case "live":
		return HarnessLive, nil
	default:
		return HarnessOff, errors.New("unknown harness mode " + s)
	}
}

// ActionRestartContainer is the entire M2-v1 Action catalog (ADR 0011). The
// catalog lives in code; config may only narrow it (ADR 0010).
const ActionRestartContainer = "restart_container"

// ActionNone means the Diagnosis proposed nothing actionable. It is the
// fail-closed default whenever the Reasoner is unreachable, times out, or
// returns garbage (ADR 0009).
const ActionNone = "none"

// ToolCall is one bounded read-only tool invocation inside a Diagnosis.
// Output is already scrubbed (ADR 0013) before it is stored or sent anywhere.
type ToolCall struct {
	Tool     string
	Args     map[string]string
	Output   string
	Err      string
	At       time.Time
	Duration time.Duration
}

// Usage is the per-Diagnosis inference cost record. It is the "usage metrics"
// surface the dashboard reports; it is recorded for local backends too.
type Usage struct {
	Backend          string
	Model            string
	PromptTokens     int
	CompletionTokens int
	Latency          time.Duration
}

// ProposedAction is what the Diagnosis proposes, in domain terms. Subject is a
// Service name from config — never a container name, host, or command
// (ADR 0018). Kind must be ActionNone or a catalog constant.
type ProposedAction struct {
	Kind      string
	Subject   string
	Rationale string
}

// Diagnosis is the read-only conclusion of one Harness cycle.
type Diagnosis struct {
	Summary  string
	Proposed ProposedAction
	Usage    Usage
	// Fallback is true when the deterministic Runbook Fallback produced this
	// Diagnosis because every configured Reasoner route failed.
	Fallback bool
}

// ApprovalDecision is the Operator gate. Zero value is Pending; a cycle that
// times out becomes Expired, which is a deny (default-deny, ADR 0009).
type ApprovalDecision int

const (
	ApprovalPending ApprovalDecision = iota
	ApprovalApproved
	ApprovalDenied
	ApprovalExpired
	ApprovalNotRequested // shadow mode, or nothing was proposed
)

func (d ApprovalDecision) String() string {
	switch d {
	case ApprovalApproved:
		return "approved"
	case ApprovalDenied:
		return "denied"
	case ApprovalExpired:
		return "expired"
	case ApprovalNotRequested:
		return "not_requested"
	default:
		return "pending"
	}
}

// Outcome adjudication values. The Actuator never grades its own homework
// (ADR 0016): only the v1 monitoring spine's committed Status decides.
const (
	OutcomeNone         = "none"          // nothing was dispatched
	OutcomePending      = "pending"       // dispatched, waiting on the v1 spine
	OutcomeRecovered    = "recovered"     // committed Status returned to UP in-window
	OutcomeActionFailed = "action_failed" // still not UP when the window closed
	OutcomeDispatchErr  = "dispatch_error"
)

// HarnessCycle is the durable, append-only accountability record for one
// incident (ADR 0019). It is exempt from the rolling Probe-history retention
// window and is never truncated by it.
type HarnessCycle struct {
	ID             string
	Subject        string
	TriggerStatus  Status
	Mode           HarnessMode
	StartedAt      time.Time
	ToolCalls      []ToolCall
	Diagnosis      Diagnosis
	Approval       ApprovalDecision
	ApprovedBy     string
	ApprovedAt     time.Time
	ResolvedTarget string
	DispatchResult string
	DispatchedAt   time.Time
	Outcome        string
	OutcomeAt      time.Time
	Error          string
}

// NewCycleID returns a random opaque cycle id. Stdlib only.
func NewCycleID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal-grade; fall back to a time id rather
		// than panicking inside a monitoring daemon.
		return "t" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

// ReasonerReply is the only thing an inference backend may return: a catalog
// Action selection plus one secondary rationale line (ADR 0009).
type ReasonerReply struct {
	Action    string
	Subject   string
	Rationale string
	Summary   string
	Usage     Usage
}

// Reasoner is the inference seam. Implementations must be fakeable in tests
// and must never receive unscrubbed evidence (ADR 0013).
type Reasoner interface {
	Name() string
	Diagnose(ctx context.Context, prompt string) (ReasonerReply, error)
	// Healthy reports whether the backend is reachable, for harness
	// self-monitoring (ADR 0015).
	Healthy(ctx context.Context) error
}

// ReadTool is one bounded read-only Diagnosis tool. The set of tools is
// defined in code; config may only narrow it (ADR 0021). A ReadTool must never
// execute LLM-authored text.
type ReadTool interface {
	Name() string
	Description() string
	Call(ctx context.Context, args map[string]string) (string, error)
}

// Actuator dispatches an approved typed Action using the separate scoped write
// credential (ADR 0022). Dispatch success means "command sent" only
// (ADR 0016).
type Actuator interface {
	RestartContainer(ctx context.Context, container string) error
	// Healthy reports whether the write credential is usable, for harness
	// self-monitoring. It must not mutate anything.
	Healthy(ctx context.Context) error
}

// ErrScrubFailed is returned when scrubbing cannot guarantee a payload is
// clean. Scrubbing is fail-closed: scrub or do not send (ADR 0013).
var ErrScrubFailed = errors.New("scrub failed: refusing to send unscrubbed evidence")

// Scrub masks every known secret in s. It is deliberately dumb and
// deterministic: literal replacement of registered secret values. An empty or
// one-character secret is rejected, because masking it would be meaningless
// and would silently leave the payload dirty.
//
// ponytail: literal masking only; add pattern-based redaction if evidence ever
// carries secrets the process does not itself hold.
func Scrub(s string, secrets []string) (string, error) {
	out := s
	for _, sec := range secrets {
		if len(sec) < 2 {
			if sec != "" {
				return "", ErrScrubFailed
			}
			continue
		}
		out = strings.ReplaceAll(out, sec, "***")
	}
	for _, sec := range secrets {
		if len(sec) >= 2 && strings.Contains(out, sec) {
			return "", ErrScrubFailed
		}
	}
	return out, nil
}

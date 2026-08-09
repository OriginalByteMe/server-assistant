// JSON API mirror of the harness incident/approval surface (ADR 0019,
// ADR 0023). DTOs are declared locally rather than adding json tags to core
// types, so the wire format never leaks into the domain model.
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"server-assistant/internal/core"
)

func registerAPIRoutes(mux *http.ServeMux, hs HarnessSource) {
	mux.HandleFunc("GET /api/incidents", handleAPIIncidentsList(hs))
	mux.HandleFunc("GET /api/incidents/{id}", handleAPIIncidentDetail(hs))
	// Method-scoped patterns: the "/" catch-all in web.go is a GET-only
	// subtree match, so a bare (any-method) pattern on a more specific path
	// is an ambiguous overlap ServeMux rejects at registration time. Each
	// handler still gates its own method too, so a wrong-method request
	// gets an explicit 405 from our own code either way.
	mux.HandleFunc("POST /api/incidents/{id}/approve", handleAPIDecision(hs, HarnessSource.Approve))
	mux.HandleFunc("POST /api/incidents/{id}/deny", handleAPIDecision(hs, HarnessSource.Deny))
	mux.HandleFunc("POST /api/harness/halt", handleAPIHalt(hs))
	mux.HandleFunc("POST /api/harness/rearm", handleAPIRearm(hs))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func handleAPIHealth(hs HarnessSource) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		dto := healthDTO{Status: "ok", HarnessMode: core.HarnessOff.String()}
		if hs != nil {
			dto.HarnessMode = hs.Mode().String()
			dto.HarnessHalted = hs.Halted()
		}
		writeJSON(w, http.StatusOK, dto)
	}
}

func handleAPIIncidentsList(hs HarnessSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()

		limit := defaultIncidentsLimit
		if q := r.URL.Query().Get("limit"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 {
				limit = n
			}
		}
		cycles, err := hs.Incidents(ctx, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		dtos := make([]incidentDTO, 0, len(cycles))
		for _, c := range cycles {
			dtos = append(dtos, toIncidentDTO(c))
		}
		writeJSON(w, http.StatusOK, dtos)
	}
}

func handleAPIIncidentDetail(hs HarnessSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()

		c, err := hs.Incident(ctx, r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, toIncidentDTO(c))
	}
}

// handleAPIDecision implements POST /api/incidents/{id}/approve and
// .../deny. decide is HarnessSource.Approve or HarnessSource.Deny, called as
// a method expression so both routes share one handler.
//
// Milestone deviation: ADR 0009 names Telegram as the production Approval
// channel. For this M2 milestone the dashboard is the Approval surface
// instead (ADR 0023) — this handler pair is that surface.
func handleAPIDecision(hs HarnessSource, decide func(HarnessSource, context.Context, string, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()

		id := r.PathValue("id")
		c, err := hs.Incident(ctx, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if c.Approval != core.ApprovalPending {
			http.Error(w, "incident is not pending approval", http.StatusConflict)
			return
		}
		who := r.FormValue("who")
		if who == "" {
			who = "operator"
		}
		if err := decide(hs, ctx, id, who); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		updated, err := hs.Incident(ctx, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toIncidentDTO(updated))
	}
}

func handleAPIHalt(hs HarnessSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		hs.Halt(r.FormValue("reason"))
		writeJSON(w, http.StatusOK, haltedDTO{Halted: hs.Halted()})
	}
}

func handleAPIRearm(hs HarnessSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		hs.Rearm()
		writeJSON(w, http.StatusOK, haltedDTO{Halted: hs.Halted()})
	}
}

type healthDTO struct {
	Status        string `json:"status"`
	HarnessMode   string `json:"harness_mode"`
	HarnessHalted bool   `json:"harness_halted"`
}

type haltedDTO struct {
	Halted bool `json:"halted"`
}

type incidentDTO struct {
	ID             string        `json:"id"`
	Subject        string        `json:"subject"`
	TriggerStatus  string        `json:"trigger_status"`
	Mode           string        `json:"mode"`
	StartedAt      string        `json:"started_at"`
	ToolCalls      []toolCallDTO `json:"tool_calls"`
	Diagnosis      diagnosisDTO  `json:"diagnosis"`
	Approval       string        `json:"approval"`
	ApprovedBy     string        `json:"approved_by"`
	ApprovedAt     *string       `json:"approved_at"`
	ResolvedTarget string        `json:"resolved_target"`
	DispatchResult string        `json:"dispatch_result"`
	DispatchedAt   *string       `json:"dispatched_at"`
	Outcome        string        `json:"outcome"`
	OutcomeAt      *string       `json:"outcome_at"`
	Error          string        `json:"error"`
}

type toolCallDTO struct {
	Tool       string            `json:"tool"`
	Args       map[string]string `json:"args"`
	Output     string            `json:"output"`
	Err        string            `json:"err"`
	At         string            `json:"at"`
	DurationMS int64             `json:"duration_ms"`
}

type diagnosisDTO struct {
	Summary  string            `json:"summary"`
	Proposed proposedActionDTO `json:"proposed"`
	Usage    usageDTO          `json:"usage"`
	Fallback bool              `json:"fallback"`
}

type proposedActionDTO struct {
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Rationale string `json:"rationale"`
}

type usageDTO struct {
	Backend          string `json:"backend"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	LatencyMS        int64  `json:"latency_ms"`
}

// rfc3339Ptr formats t for JSON, or nil when t never happened — the contract
// requires a present-but-null key, never an omitted one, so callers must not
// add `omitempty`.
func rfc3339Ptr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

func toIncidentDTO(c core.HarnessCycle) incidentDTO {
	calls := make([]toolCallDTO, 0, len(c.ToolCalls))
	for _, tc := range c.ToolCalls {
		args := tc.Args
		if args == nil {
			args = map[string]string{}
		}
		calls = append(calls, toolCallDTO{
			Tool:       tc.Tool,
			Args:       args,
			Output:     tc.Output,
			Err:        tc.Err,
			At:         tc.At.Format(time.RFC3339),
			DurationMS: tc.Duration.Milliseconds(),
		})
	}
	return incidentDTO{
		ID:            c.ID,
		Subject:       c.Subject,
		TriggerStatus: c.TriggerStatus.String(),
		Mode:          c.Mode.String(),
		StartedAt:     c.StartedAt.Format(time.RFC3339),
		ToolCalls:     calls,
		Diagnosis: diagnosisDTO{
			Summary: c.Diagnosis.Summary,
			Proposed: proposedActionDTO{
				Kind:      c.Diagnosis.Proposed.Kind,
				Subject:   c.Diagnosis.Proposed.Subject,
				Rationale: c.Diagnosis.Proposed.Rationale,
			},
			Usage: usageDTO{
				Backend:          c.Diagnosis.Usage.Backend,
				Model:            c.Diagnosis.Usage.Model,
				PromptTokens:     c.Diagnosis.Usage.PromptTokens,
				CompletionTokens: c.Diagnosis.Usage.CompletionTokens,
				LatencyMS:        c.Diagnosis.Usage.Latency.Milliseconds(),
			},
			Fallback: c.Diagnosis.Fallback,
		},
		Approval:       c.Approval.String(),
		ApprovedBy:     c.ApprovedBy,
		ApprovedAt:     rfc3339Ptr(c.ApprovedAt),
		ResolvedTarget: c.ResolvedTarget,
		DispatchResult: c.DispatchResult,
		DispatchedAt:   rfc3339Ptr(c.DispatchedAt),
		Outcome:        c.Outcome,
		OutcomeAt:      rfc3339Ptr(c.OutcomeAt),
		Error:          c.Error,
	}
}

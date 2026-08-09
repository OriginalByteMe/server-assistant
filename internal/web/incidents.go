// Harness incident surface (ADR 0009, ADR 0019): server-rendered HTML for
// the append-only HarnessCycle record and the Operator Approval gate. Same
// conventions as web.go — html/template only, no client-side framework.
package web

import (
	"context"
	"html/template"
	"net/http"
	"time"

	"server-assistant/internal/core"
)

// handlerTimeout bounds every harness handler's Store/HarnessSource call so a
// stuck backend cannot hang an HTTP request indefinitely.
const handlerTimeout = 5 * time.Second

// defaultIncidentsLimit caps the incident list when no ?limit= is given.
const defaultIncidentsLimit = 100

// HarnessSource is the harness read/control surface the dashboard renders
// against — satisfied by *harness.Harness.
type HarnessSource interface {
	Mode() core.HarnessMode
	Halted() bool
	Halt(reason string)
	Rearm()
	Incidents(ctx context.Context, limit int) ([]core.HarnessCycle, error)
	Incident(ctx context.Context, id string) (core.HarnessCycle, error)
	Approve(ctx context.Context, id, who string) error
	Deny(ctx context.Context, id, who string) error
}

// HandlerWithHarness returns the dashboard mux with the harness incident
// list/detail pages and the JSON API (api.go) additionally wired in. Handler
// keeps working unchanged for the harness-blind caller in main.go; both
// delegate to buildMux (web.go) so routing lives in exactly one place.
func HandlerWithHarness(vs ViewSource, hs HarnessSource) http.Handler {
	return buildMux(vs, hs, nil, nil)
}

func registerIncidentRoutes(mux *http.ServeMux, hs HarnessSource) {
	mux.HandleFunc("GET /incidents", func(w http.ResponseWriter, r *http.Request) {
		handleIncidentsList(w, r, hs)
	})
	mux.HandleFunc("GET /incidents/{id}", func(w http.ResponseWriter, r *http.Request) {
		handleIncidentDetail(w, r, hs)
	})
}

func handleIncidentsList(w http.ResponseWriter, r *http.Request, hs HarnessSource) {
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()

	cycles, err := hs.Incidents(ctx, defaultIncidentsLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]incidentRow, 0, len(cycles))
	for _, c := range cycles {
		rows = append(rows, incidentRowOf(c))
	}
	data := incidentsListData{Halted: hs.Halted(), Mode: hs.Mode().String(), Rows: rows}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := incidentsListTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func handleIncidentDetail(w http.ResponseWriter, r *http.Request, hs HarnessSource) {
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()

	c, err := hs.Incident(ctx, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := incidentDetailTmpl.Execute(w, incidentDetailViewOf(c)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// timeOrDash formats a time for HTML display, matching web.go's rowOf
// convention of an em-dash placeholder for a time that never happened.
func timeOrDash(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format(time.RFC3339)
}

// pendingIncident feeds the dashboard's top-of-page alert banner (ADR 0023):
// a cycle awaiting an Operator decision must be impossible to miss.
type pendingIncident struct {
	ID             string
	Subject        string
	TriggerStatus  string
	ProposedAction string
}

// pendingIncidentsFor returns every cycle still awaiting an Approval
// decision, newest first. Best-effort by design: a nil harness or a store
// error yields no banner rather than a broken dashboard.
func pendingIncidentsFor(ctx context.Context, hs HarnessSource) []pendingIncident {
	if hs == nil {
		return nil
	}
	cycles, err := hs.Incidents(ctx, defaultIncidentsLimit)
	if err != nil {
		return nil
	}
	var out []pendingIncident
	for _, c := range cycles {
		if c.Approval == core.ApprovalPending {
			out = append(out, pendingIncident{
				ID:             c.ID,
				Subject:        c.Subject,
				TriggerStatus:  c.TriggerStatus.String(),
				ProposedAction: c.Diagnosis.Proposed.Kind,
			})
		}
	}
	return out
}

type incidentRow struct {
	ID             string
	StartedAt      string
	Subject        string
	TriggerStatus  string
	Mode           string
	ProposedAction string
	Approval       string
	// Pending highlights the row: this incident still needs a decision.
	Pending bool
	Outcome string
}

func incidentRowOf(c core.HarnessCycle) incidentRow {
	return incidentRow{
		ID:             c.ID,
		StartedAt:      timeOrDash(c.StartedAt),
		Subject:        c.Subject,
		TriggerStatus:  c.TriggerStatus.String(),
		Mode:           c.Mode.String(),
		ProposedAction: c.Diagnosis.Proposed.Kind,
		Approval:       c.Approval.String(),
		Pending:        c.Approval == core.ApprovalPending,
		Outcome:        c.Outcome,
	}
}

type incidentsListData struct {
	Halted bool
	Mode   string
	Rows   []incidentRow
}

type toolCallView struct {
	Tool       string
	Args       map[string]string
	Output     string
	Err        string
	At         string
	DurationMS int64
}

// incidentDetailView is the detail page's template root — every value is
// pre-formatted in Go (matching web.go's rowOf convention) so the template
// itself does no formatting, only escaping.
type incidentDetailView struct {
	ID               string
	Subject          string
	TriggerStatus    string
	Mode             string
	StartedAt        string
	ToolCalls        []toolCallView
	Summary          string
	ProposedKind     string
	ProposedSubject  string
	Rationale        string
	Fallback         bool
	UsageBackend     string
	UsageModel       string
	PromptTokens     int
	CompletionTokens int
	LatencyMS        int64
	Approval         string
	Pending          bool
	ApprovedBy       string
	ApprovedAt       string
	ResolvedTarget   string
	DispatchResult   string
	DispatchedAt     string
	// DispatchFailed styles a failed dispatch as an error instead of the
	// neutral "Dispatched at: —" it would otherwise read as (display only;
	// the stored cycle is untouched).
	DispatchFailed bool
	Outcome        string
	OutcomeAt      string
	Error          string
}

func incidentDetailViewOf(c core.HarnessCycle) incidentDetailView {
	calls := make([]toolCallView, 0, len(c.ToolCalls))
	for _, tc := range c.ToolCalls {
		calls = append(calls, toolCallView{
			Tool:       tc.Tool,
			Args:       tc.Args,
			Output:     tc.Output,
			Err:        tc.Err,
			At:         timeOrDash(tc.At),
			DurationMS: tc.Duration.Milliseconds(),
		})
	}
	return incidentDetailView{
		ID:               c.ID,
		Subject:          c.Subject,
		TriggerStatus:    c.TriggerStatus.String(),
		Mode:             c.Mode.String(),
		StartedAt:        timeOrDash(c.StartedAt),
		ToolCalls:        calls,
		Summary:          c.Diagnosis.Summary,
		ProposedKind:     c.Diagnosis.Proposed.Kind,
		ProposedSubject:  c.Diagnosis.Proposed.Subject,
		Rationale:        c.Diagnosis.Proposed.Rationale,
		Fallback:         c.Diagnosis.Fallback,
		UsageBackend:     c.Diagnosis.Usage.Backend,
		UsageModel:       c.Diagnosis.Usage.Model,
		PromptTokens:     c.Diagnosis.Usage.PromptTokens,
		CompletionTokens: c.Diagnosis.Usage.CompletionTokens,
		LatencyMS:        c.Diagnosis.Usage.Latency.Milliseconds(),
		Approval:         c.Approval.String(),
		Pending:          c.Approval == core.ApprovalPending,
		ApprovedBy:       c.ApprovedBy,
		ApprovedAt:       timeOrDash(c.ApprovedAt),
		ResolvedTarget:   c.ResolvedTarget,
		DispatchResult:   c.DispatchResult,
		DispatchedAt:     timeOrDash(c.DispatchedAt),
		DispatchFailed:   c.Outcome == core.OutcomeDispatchErr,
		Outcome:          c.Outcome,
		OutcomeAt:        timeOrDash(c.OutcomeAt),
		Error:            c.Error,
	}
}

var incidentsListTmpl = template.Must(template.New("incidents").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Incidents — Server Assistant</title>
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<h1>Incidents</h1>
{{- if .Halted }}
<p class="harness-halted">HARNESS HALTED</p>
{{- end }}
<p>Mode: <strong>{{ .Mode }}</strong></p>
<table>
<thead><tr><th>Started</th><th>Subject</th><th>Trigger</th><th>Mode</th><th>Proposed</th><th>Approval</th><th>Outcome</th><th></th></tr></thead>
<tbody>
{{- range .Rows }}
<tr{{ if .Pending }} class="row-pending"{{ end }}><td>{{ .StartedAt }}</td><td>{{ .Subject }}</td><td><span class="s-{{ .TriggerStatus }}">{{ .TriggerStatus }}</span></td><td>{{ .Mode }}</td><td>{{ .ProposedAction }}</td><td><span class="a-{{ .Approval }}">{{ .Approval }}</span></td><td><span class="o-{{ .Outcome }}">{{ .Outcome }}</span></td><td><a href="/incidents/{{ .ID }}">{{ if .Pending }}decide{{ else }}detail{{ end }}</a></td></tr>
{{- end }}
</tbody>
</table>
<p><a href="/">Dashboard</a></p>
</body>
</html>`))

var incidentDetailTmpl = template.Must(template.New("incident-detail").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Incident {{ .ID }} — Server Assistant</title>
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<h1>Incident {{ .ID }}</h1>
<p><a href="/incidents">&larr; Incidents</a></p>

<h2>Facts</h2>
<p>Subject: <strong>{{ .Subject }}</strong> — Trigger status: <strong class="s-{{ .TriggerStatus }}">{{ .TriggerStatus }}</strong> — Mode: <strong>{{ .Mode }}</strong> — Started: {{ .StartedAt }}</p>
{{- range .ToolCalls }}
<h3>{{ .Tool }} <small class="muted">({{ .DurationMS }} ms, {{ .At }})</small></h3>
{{- if .Output }}
<pre class="tool-output">{{ .Output }}</pre>
{{- else }}
<p class="muted">(no output)</p>
{{- end }}
{{- if .Err }}
<p class="err">error: {{ .Err }}</p>
{{- end }}
{{- end }}

<h2>Diagnosis</h2>
{{- if .Fallback }}
<p><span class="badge">fallback</span></p>
{{- end }}
<p>{{ .Summary }}</p>
<p>Proposed: <strong>{{ .ProposedKind }}</strong> on <strong>{{ .ProposedSubject }}</strong></p>
<p>Rationale: {{ .Rationale }}</p>

<h2>Usage</h2>
<p>Backend: {{ .UsageBackend }} — Model: {{ .UsageModel }} — Prompt tokens: {{ .PromptTokens }} — Completion tokens: {{ .CompletionTokens }} — Latency: {{ .LatencyMS }} ms</p>

<h2>Approval</h2>
{{- if .Pending }}
<form class="inline" method="post" action="/api/incidents/{{ .ID }}/approve"><button class="btn btn-primary" type="submit">Approve</button></form>
<form class="inline" method="post" action="/api/incidents/{{ .ID }}/deny"><button class="btn btn-danger" type="submit">Deny</button></form>
{{- else }}
<p>Decision: <strong class="a-{{ .Approval }}">{{ .Approval }}</strong>{{ if .ApprovedBy }} by {{ .ApprovedBy }}{{ end }} at {{ .ApprovedAt }}</p>
{{- end }}

<h2>Dispatch</h2>
<p>Resolved target: {{ if .ResolvedTarget }}{{ .ResolvedTarget }}{{ else }}<span class="muted">—</span>{{ end }}</p>
{{- if .DispatchFailed }}
<p class="err"><strong>Dispatch attempted and failed:</strong> {{ .DispatchResult }}</p>
{{- else if .DispatchResult }}
<p>Dispatch result: {{ .DispatchResult }}</p>
<p>Dispatched at: {{ .DispatchedAt }}</p>
{{- else }}
<p class="muted">Nothing dispatched.</p>
{{- end }}

<h2>Outcome</h2>
{{- if .Outcome }}
<p><span class="o-{{ .Outcome }}">{{ .Outcome }}</span> at {{ .OutcomeAt }}</p>
{{- else }}
<p class="muted"><em>Not yet adjudicated — only the monitoring spine's next committed Status decides this (ADR 0016).</em></p>
{{- end }}
{{- if .Error }}
<p class="err">Error: {{ .Error }}</p>
{{- end }}
</body>
</html>`))

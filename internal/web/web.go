// Package web serves the read-only dashboard: a server-rendered Service list
// plus a Server-Sent Events stream. Live updates use vendored HTMX and its
// SSE extension (designated in docs/CONVENTIONS.md — vendored, embedded, no
// build step); the SSE stream emits HTML row fragments that HTMX swaps in.
package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"server-assistant/internal/core"
)

//go:embed static/htmx.min.js static/sse.js static/style.css
var staticFS embed.FS

// ViewSource is the dashboard's read model — satisfied by *monitor.Monitor.
type ViewSource interface {
	Snapshot() []core.ServiceView
	Subscribe() (<-chan core.ServiceView, func())
	// History returns a subject's recent Probe samples (oldest→newest) for
	// the trend sparkline (ARK-9).
	History(name string) []core.ProbeSample
}

// Handler returns the dashboard mux: the page at /, vendored assets under
// /static/, and the SSE stream at /events.
func Handler(vs ViewSource) http.Handler {
	return buildMux(vs, nil)
}

// buildMux assembles the dashboard mux for both the harness-blind
// constructor (Handler) and the harness-aware one (HandlerWithHarness,
// incidents.go). Routing lives in exactly one place; a nil hs simply means
// the harness-specific patterns are never registered, so ServeMux 404s them
// naturally and the dashboard renders no Harness panel.
func buildMux(vs ViewSource, hs HarnessSource) *http.ServeMux {
	assets, _ := fs.Sub(staticFS, "static")

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(assets)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()
		data := pageData{
			Rows:    rowsWithHistory(vs),
			Harness: harnessPanelFor(hs),
			Pending: pendingIncidentsFor(ctx, hs),
		}
		if err := pageTmpl.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, r, vs)
	})
	// /api/health is always registered and nil-safe (ADR 0023): the
	// dashboard's liveness probe works even when no harness is wired in.
	mux.HandleFunc("GET /api/health", handleAPIHealth(hs))
	if hs != nil {
		registerIncidentRoutes(mux, hs)
		registerAPIRoutes(mux, hs)
	}
	return mux
}

func serveSSE(w http.ResponseWriter, r *http.Request, vs ViewSource) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := vs.Subscribe()
	defer cancel()

	// Initial paint so a freshly opened tab is current without waiting for a
	// probe tick.
	for _, v := range vs.Snapshot() {
		writeEvent(w, vs, v)
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case v, open := <-ch:
			if !open {
				return
			}
			writeEvent(w, vs, v)
			flusher.Flush()
		}
	}
}

// writeEvent emits one named SSE event whose data is the Service's HTML row
// cells. HTMX's sse-swap on the matching <tr> replaces its contents. The
// fragment is forced onto a single line so it is one SSE data field.
func writeEvent(w http.ResponseWriter, vs ViewSource, v core.ServiceView) {
	var b strings.Builder
	if err := cellsTmpl.Execute(&b, rowOf(v, sparkline(vs.History(v.Name)))); err != nil {
		return
	}
	frag := strings.ReplaceAll(b.String(), "\n", "")
	_, _ = fmt.Fprintf(w, "event: svc-%s\ndata: %s\n\n", v.Name, frag)
}

type row struct {
	Name        string
	Status      string
	LatencyMS   int64
	LastChecked string
	Spark       template.HTML
}

func rowOf(v core.ServiceView, spark template.HTML) row {
	last := "—"
	if !v.LastChecked.IsZero() {
		last = v.LastChecked.Format(time.RFC3339)
	}
	return row{
		Name:        v.Name,
		Status:      v.Status.String(),
		LatencyMS:   v.Latency.Milliseconds(),
		LastChecked: last,
		Spark:       spark,
	}
}

// rowsWithHistory builds the server-rendered rows, attaching each subject's
// trend sparkline from its recent Probe history (ARK-9).
func rowsWithHistory(vs ViewSource) []row {
	snap := vs.Snapshot()
	out := make([]row, 0, len(snap))
	for _, v := range snap {
		out = append(out, rowOf(v, sparkline(vs.History(v.Name))))
	}
	return out
}

// pageData is the dashboard template's root. Harness is nil whenever no
// HarnessSource was wired in (Handler, not HandlerWithHarness) — the
// template renders no Harness panel in that case.
type pageData struct {
	Rows    []row
	Harness *harnessPanel
	// Pending drives the top-of-page alert banner (ADR 0023): every cycle
	// still awaiting an Operator decision, newest first.
	Pending []pendingIncident
}

type harnessPanel struct {
	Mode   string
	Halted bool
}

func harnessPanelFor(hs HarnessSource) *harnessPanel {
	if hs == nil {
		return nil
	}
	return &harnessPanel{Mode: hs.Mode().String(), Halted: hs.Halted()}
}

// sparkline renders an inline SVG latency trend coloured by the latest
// Status — no JavaScript, no build step (ADR 0004; vendored/embedded only).
// An empty history is a typographic placeholder so the cell is never broken
// or missing. Latencies are normalised to the window's own max so the shape
// is meaningful regardless of absolute scale.
func sparkline(samples []core.ProbeSample) template.HTML {
	if len(samples) == 0 {
		return template.HTML(`<span class="spark-none">—</span>`)
	}
	const w, h = 120.0, 24.0
	var maxNS int64 = 1
	for _, s := range samples {
		if int64(s.Latency) > maxNS {
			maxNS = int64(s.Latency)
		}
	}
	var pts strings.Builder
	n := len(samples)
	for i, s := range samples {
		x := 0.0
		if n > 1 {
			x = float64(i) / float64(n-1) * w
		}
		// Invert Y: lower latency draws nearer the top.
		y := h - (float64(s.Latency)/float64(maxNS))*(h-2) - 1
		if i > 0 {
			pts.WriteByte(' ')
		}
		fmt.Fprintf(&pts, "%.1f,%.1f", x, y)
	}
	stroke := "#137333" // UP
	switch samples[len(samples)-1].Status {
	case core.StatusDegraded:
		stroke = "#b06000"
	case core.StatusDown:
		stroke = "#c5221f"
	case core.StatusUnknown:
		stroke = "#5f6368"
	}
	svg := fmt.Sprintf(
		`<svg class="spark-svg" width="%d" height="%d" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="latency trend"><polyline fill="none" stroke="%s" stroke-width="1.5" points="%s"/></svg>`,
		int(w), int(h), int(w), int(h), stroke, pts.String())
	return template.HTML(svg) //nolint:gosec // all interpolated values are numeric or a fixed enum, never user input
}

// cellsTmpl is the single source of truth for one row's cells — used both for
// the server-rendered page and the SSE swap fragment. Kept on one logical
// line so it survives newline-stripping into an SSE data field.
var cellsTmpl = template.Must(template.New("cells").Parse(
	`<td class="name">{{ .Name }}</td><td class="status s-{{ .Status }}">{{ .Status }}</td><td class="latency">{{ .LatencyMS }} ms</td><td class="checked">{{ .LastChecked }}</td><td class="spark">{{ .Spark }}</td>`))

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Server Assistant</title>
<link rel="stylesheet" href="/static/style.css">
<script src="/static/htmx.min.js"></script>
<script src="/static/sse.js"></script>
</head>
<body hx-ext="sse" sse-connect="/events">
{{- range .Pending }}
<div class="alert-pending" role="alert"><strong>Incident awaiting your decision:</strong> {{ .Subject }} is <span class="s-{{ .TriggerStatus }}">{{ .TriggerStatus }}</span> — proposed action <strong>{{ .ProposedAction }}</strong>.<a class="btn btn-primary" href="/incidents/{{ .ID }}">Review &amp; decide</a></div>
{{- end }}
<h1>Server Assistant</h1>
<table>
<thead><tr><th>Service</th><th>Status</th><th>Latency</th><th>Last checked</th><th>Trend</th></tr></thead>
<tbody>
{{- range .Rows }}
<tr id="svc-{{ .Name }}" sse-swap="svc-{{ .Name }}"><td class="name">{{ .Name }}</td><td class="status s-{{ .Status }}">{{ .Status }}</td><td class="latency">{{ .LatencyMS }} ms</td><td class="checked">{{ .LastChecked }}</td><td class="spark">{{ .Spark }}</td></tr>
{{- end }}
</tbody>
</table>
{{- if .Harness }}
<h2>Harness</h2>
{{- if .Harness.Halted }}
<p class="harness-halted">HALTED</p>
{{- end }}
<p>Mode: <strong>{{ .Harness.Mode }}</strong></p>
<form class="inline" method="post" action="/api/harness/halt"><button class="btn btn-danger" type="submit">Halt</button></form>
<form class="inline" method="post" action="/api/harness/rearm" onsubmit="return confirm('Re-arm the harness?')"><button class="btn btn-secondary" type="submit">Re-arm</button></form>
<p><a href="/incidents">Incidents</a></p>
{{- end }}
</body>
</html>`))

// Package web serves the read-only dashboard: a server-rendered Service list
// plus a Server-Sent Events stream. Live updates use vendored HTMX and its
// SSE extension (designated in docs/CONVENTIONS.md — vendored, embedded, no
// build step); the SSE stream emits HTML row fragments that HTMX swaps in.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"server-assistant/internal/core"
)

//go:embed static/htmx.min.js static/sse.js
var staticFS embed.FS

// ViewSource is the dashboard's read model — satisfied by *monitor.Monitor.
type ViewSource interface {
	Snapshot() []core.ServiceView
	Subscribe() (<-chan core.ServiceView, func())
}

// Handler returns the dashboard mux: the page at /, vendored assets under
// /static/, and the SSE stream at /events.
func Handler(vs ViewSource) http.Handler {
	assets, _ := fs.Sub(staticFS, "static")

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(assets)))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageTmpl.Execute(w, vs.Snapshot()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, r, vs)
	})
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
		writeEvent(w, v)
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
			writeEvent(w, v)
			flusher.Flush()
		}
	}
}

// writeEvent emits one named SSE event whose data is the Service's HTML row
// cells. HTMX's sse-swap on the matching <tr> replaces its contents. The
// fragment is forced onto a single line so it is one SSE data field.
func writeEvent(w http.ResponseWriter, v core.ServiceView) {
	var b strings.Builder
	if err := cellsTmpl.Execute(&b, rowOf(v)); err != nil {
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
}

func rowOf(v core.ServiceView) row {
	last := "—"
	if !v.LastChecked.IsZero() {
		last = v.LastChecked.Format(time.RFC3339)
	}
	return row{
		Name:        v.Name,
		Status:      v.Status.String(),
		LatencyMS:   v.Latency.Milliseconds(),
		LastChecked: last,
	}
}

func rows(vs []core.ServiceView) []row {
	out := make([]row, 0, len(vs))
	for _, v := range vs {
		out = append(out, rowOf(v))
	}
	return out
}

// cellsTmpl is the single source of truth for one row's cells — used both for
// the server-rendered page and the SSE swap fragment. Kept on one logical
// line so it survives newline-stripping into an SSE data field.
var cellsTmpl = template.Must(template.New("cells").Parse(
	`<td class="name">{{ .Name }}</td><td class="status s-{{ .Status }}">{{ .Status }}</td><td class="latency">{{ .LatencyMS }} ms</td><td class="checked">{{ .LastChecked }}</td>`))

var pageTmpl = template.Must(template.New("page").Funcs(template.FuncMap{
	"rows": rows,
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Server Assistant</title>
<script src="/static/htmx.min.js"></script>
<script src="/static/sse.js"></script>
<style>
 body{font:14px system-ui,sans-serif;margin:2rem;color:#1a1a1a}
 table{border-collapse:collapse;min-width:32rem}
 th,td{text-align:left;padding:.5rem .9rem;border-bottom:1px solid #ddd}
 .s-UP{color:#137333;font-weight:600}
 .s-DEGRADED{color:#b06000;font-weight:600}
 .s-DOWN{color:#c5221f;font-weight:600}
 .s-UNKNOWN{color:#5f6368;font-weight:600}
</style>
</head>
<body hx-ext="sse" sse-connect="/events">
<h1>Server Assistant</h1>
<table>
<thead><tr><th>Service</th><th>Status</th><th>Latency</th><th>Last checked</th></tr></thead>
<tbody>
{{- range rows . }}
<tr id="svc-{{ .Name }}" sse-swap="svc-{{ .Name }}"><td class="name">{{ .Name }}</td><td class="status s-{{ .Status }}">{{ .Status }}</td><td class="latency">{{ .LatencyMS }} ms</td><td class="checked">{{ .LastChecked }}</td></tr>
{{- end }}
</tbody>
</table>
</body>
</html>`))

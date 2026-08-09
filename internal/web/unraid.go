// unraid.go — the live Unraid state page (GitHub #53/#57): CPU/memory/
// uptime, array + parity, per-disk capacity/temperature/SMART verdict,
// share capacity, container state, and the reachability self-check. Same
// html/template-only convention as web.go/incidents.go — no client-side
// framework, HTMX polling only (ADR 0004/CONVENTIONS: vendored, no build
// step).
//
// CONVENTIONS rule 5 governs every section: a core.UnraidSource read that
// fails renders an explicit message, never zeros standing in for real
// data — core.ErrUnauthenticated in particular, since no Unraid API key
// exists yet (docs/research/unraid-state-sources.md).
package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"server-assistant/internal/core"
)

// unauthHint is what a human sees (and what /api/unraid/* echoes) for
// core.ErrUnauthenticated: creating the API key is a host mutation awaiting
// the human's approval, not a bug to route around.
const unauthHint = "Not authenticated against the Unraid API — no API key exists yet; creating one is a host action awaiting approval. Once approved and configured, restart Server Assistant."

// standbyTemp replaces a spun-down disk's temperature — core.ErrDiskStandby
// exists so the sampler never wakes a sleeping disk just to read it, and the
// dashboard must not paper over that gap with a fake 0°C.
const standbyTemp = "spun down, not woken"

// HandlerFull returns the dashboard mux with the harness incident surface,
// the live Unraid /unraid page + JSON mirror, and — when ps is non-nil —
// the script-proposal Approval surface, all wired in. Handler and
// HandlerWithHarness (incidents.go) keep working unchanged for callers that
// don't have Unraid state yet; all three delegate to buildMux (web.go) so
// routing lives in exactly one place. ps may be nil until the real proposal
// registry lands (another ticket) — same nil-means-absent convention as hs.
func HandlerFull(vs ViewSource, hs HarnessSource, us core.UnraidSource, ps ProposalSource) http.Handler {
	return buildMux(vs, hs, us, ps)
}

func registerUnraidRoutes(mux *http.ServeMux, us core.UnraidSource, ps ProposalSource) {
	mux.HandleFunc("GET /unraid", func(w http.ResponseWriter, r *http.Request) {
		handleUnraidPage(w, r, us, ps)
	})
	registerUnraidAPIRoutes(mux, us)
	if ps != nil {
		mux.HandleFunc("POST /api/unraid/proposals/{id}/approve", handleAPIProposalDecision(ps, ProposalSource.Approve))
		mux.HandleFunc("POST /api/unraid/proposals/{id}/deny", handleAPIProposalDecision(ps, ProposalSource.Deny))
	}
}

func handleUnraidPage(w http.ResponseWriter, r *http.Request, us core.UnraidSource, ps ProposalSource) {
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()

	data := unraidPageData{
		Reach:      reachViewOf(ctx, us),
		Host:       hostSectionOf(ctx, us),
		Array:      arraySectionOf(ctx, us),
		Shares:     sharesSectionOf(ctx, us),
		Containers: containersSectionOf(ctx, us),
		Proposals:  proposalRowsOf(ctx, ps),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := unraidPageTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// errMessage turns a UnraidSource failure into the exact text the dashboard
// shows in place of the failed section — core.ErrUnauthenticated gets the
// actionable hint, anything else is surfaced verbatim (rule 5: never hide
// it, never zero it).
func errMessage(err error) string {
	if errors.Is(err, core.ErrUnauthenticated) {
		return unauthHint
	}
	return err.Error()
}

// unraidPageData is the /unraid template's root. Every section is
// independently OK/failed: one section erroring (e.g. Shares) must not blank
// out the sections that succeeded (e.g. Host).
type unraidPageData struct {
	Reach      reachView
	Host       hostSection
	Array      arraySection
	Shares     sharesSection
	Containers containersSection
	// Proposals is nil with no ProposalSource wired in (or on a read
	// error) — the section simply doesn't render, matching the Harness
	// panel's nil-means-absent convention.
	Proposals []proposalRow
}

// reachView renders core.Reachability's four-way ReachState distinctly
// (GitHub #51/#56) — ReachTailnet in particular must say plainly that a
// cloud-hosted LLM cannot reach this endpoint, since that is the silent
// failure mode the product exists to avoid.
type reachView struct {
	OK         bool
	State      string
	Headline   string
	Detail     string
	PublicURL  string
	TailnetURL string
	Err        string
}

func reachViewOf(ctx context.Context, us core.UnraidSource) reachView {
	rc, err := us.Reachability(ctx)
	if err != nil {
		return reachView{State: "error", Headline: "Reachability check failed", Err: errMessage(err)}
	}
	v := reachView{OK: true, State: string(rc.State), Detail: rc.Detail, PublicURL: rc.PublicURL, TailnetURL: rc.TailnetURL}
	switch rc.State {
	case core.ReachAbsent:
		v.Headline = "No Tailscale on this Host — reachable on the LAN only."
	case core.ReachTailnet:
		v.Headline = "Reachable on the tailnet only — a cloud-hosted LLM cannot reach this endpoint."
	case core.ReachFunnel:
		v.Headline = "Served publicly via Tailscale Funnel."
	case core.ReachFailing:
		v.Headline = "Configured endpoint is not answering."
	default:
		v.Headline = "Unrecognised reachability state."
	}
	return v
}

type hostSection struct {
	OK   bool
	Err  string
	Data hostView
}

type hostView struct {
	Hostname      string
	UnraidVersion string
	CPUModel      string
	CPUCores      int
	CPUPercent    string
	MemUsed       string
	MemTotal      string
	Uptime        string
	// Degraded is true when these vitals came from the Host's bind-mounted
	// procfs rather than unraid-api. The reading itself is real — CPU is a
	// genuine delta-sampled measurement, not an estimate — so the notice
	// says "different source", never "less trustworthy number".
	Degraded    bool
	CollectedAt string
}

func hostSectionOf(ctx context.Context, us core.UnraidSource) hostSection {
	h, err := us.HostInfo(ctx)
	if err != nil {
		return hostSection{Err: errMessage(err)}
	}
	return hostSection{OK: true, Data: hostView{
		Hostname:      h.Hostname,
		UnraidVersion: h.UnraidVersion,
		CPUModel:      h.CPUModel,
		CPUCores:      h.CPUCores,
		CPUPercent:    fmt.Sprintf("%.1f%%", h.CPUPercent),
		MemUsed:       formatBytes(h.MemUsedBytes),
		MemTotal:      formatBytes(h.MemTotalBytes),
		Uptime:        formatUptime(h.UptimeSeconds),
		Degraded:      h.Source == core.SourceProcfs,
		CollectedAt:   timeOrDash(h.CollectedAt),
	}}
}

type arraySection struct {
	OK   bool
	Err  string
	Data arrayView
}

type arrayView struct {
	State            string
	ParityActive     bool
	ParityProgress   string
	ParityLastCheck  string
	ParityLastErrors int64
	Disks            []diskView
	// Degraded is true when this reading came from the emhttp INI fallback
	// rather than unraid-api. The panel says so, because a user comparing a
	// sparse array view against a full one must be able to tell "no parity
	// data on this machine" from "we read this the cheap way".
	Degraded    bool
	CollectedAt string
}

// diskView never lets a spun-down disk's TempC render as a bogus reading —
// core.ErrDiskStandby's own wording ("spun down, not woken") is the exact
// text shown, matching the sampler's own reason for not reading it.
type diskView struct {
	Name        string
	Role        string
	Size        string
	Used        string
	Temp        string
	Standby     bool
	SmartStatus string
}

func arraySectionOf(ctx context.Context, us core.UnraidSource) arraySection {
	a, err := us.Array(ctx)
	if err != nil {
		return arraySection{Err: errMessage(err)}
	}
	disks := make([]diskView, 0, len(a.Disks))
	for _, d := range a.Disks {
		disks = append(disks, diskViewOf(d))
	}
	lastCheck := "—"
	if a.ParityLastCheck != nil {
		lastCheck = timeOrDash(*a.ParityLastCheck)
	}
	return arraySection{OK: true, Data: arrayView{
		State:            a.State,
		ParityActive:     a.ParityCheckActive,
		ParityProgress:   fmt.Sprintf("%.1f%%", a.ParityCheckProgress),
		ParityLastCheck:  lastCheck,
		ParityLastErrors: a.ParityLastErrors,
		Disks:            disks,
		Degraded:         a.Source == core.SourceEmhttp,
		CollectedAt:      timeOrDash(a.CollectedAt),
	}}
}

func diskViewOf(d core.Disk) diskView {
	temp := "unknown"
	switch {
	case d.SpunDown:
		temp = standbyTemp
	case d.TempC != nil:
		temp = fmt.Sprintf("%d°C", *d.TempC)
	}
	return diskView{
		Name:        d.Name,
		Role:        d.Role,
		Size:        formatBytes(d.SizeBytes),
		Used:        formatBytes(d.UsedBytes),
		Temp:        temp,
		Standby:     d.SpunDown,
		SmartStatus: d.SmartStatus,
	}
}

type sharesSection struct {
	OK   bool
	Err  string
	Data []shareView
}

type shareView struct {
	Name       string
	Size       string
	Used       string
	Free       string
	Allocator  string
	CachePool  string
	Exported   bool
	Accessible bool
}

func sharesSectionOf(ctx context.Context, us core.UnraidSource) sharesSection {
	shares, err := us.Shares(ctx)
	if err != nil {
		return sharesSection{Err: errMessage(err)}
	}
	out := make([]shareView, 0, len(shares))
	for _, s := range shares {
		out = append(out, shareView{
			Name:       s.Name,
			Size:       formatBytes(s.SizeBytes),
			Used:       formatBytes(s.UsedBytes),
			Free:       formatBytes(s.FreeBytes),
			Allocator:  s.Allocator,
			CachePool:  s.CachePool,
			Exported:   s.Exported,
			Accessible: s.Accessible,
		})
	}
	return sharesSection{OK: true, Data: out}
}

type containersSection struct {
	OK   bool
	Err  string
	Data []containerView
}

type containerView struct {
	Name    string
	Image   string
	State   string
	Status  string
	Ports   string
	AutoRun bool
}

func containersSectionOf(ctx context.Context, us core.UnraidSource) containersSection {
	containers, err := us.Containers(ctx)
	if err != nil {
		return containersSection{Err: errMessage(err)}
	}
	out := make([]containerView, 0, len(containers))
	for _, c := range containers {
		out = append(out, containerView{
			Name:    c.Name,
			Image:   c.Image,
			State:   c.State,
			Status:  c.Status,
			Ports:   strings.Join(c.Ports, ", "),
			AutoRun: c.AutoRun,
		})
	}
	return containersSection{OK: true, Data: out}
}

// formatBytes renders a byte count the way an Unraid user reads capacity —
// binary units, one decimal place. Stdlib only.
func formatBytes(n int64) string {
	const unit = 1024.0
	if n < int64(unit) {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := unit, 0
	for v := float64(n) / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/div, "KMGTP"[exp])
}

// formatUptime renders a duration the way an operator scans it: the coarsest
// two units, never more.
func formatUptime(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

var unraidPageTmpl = template.Must(template.New("unraid").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Unraid — Server Assistant</title>
<link rel="stylesheet" href="/static/style.css">
<script src="/static/htmx.min.js"></script>
</head>
<body>
<p><a href="/">&larr; Dashboard</a></p>
<h1>Unraid</h1>

<div class="alert-unauth" role="alert"><strong>No authentication:</strong> this Approval surface is <strong>unauthenticated</strong> — anyone who can reach this page can approve or deny proposed actions. Development proceeds this way by explicit decision; login (HL-SA-10) is on hold.</div>

<div class="reach-banner reach-{{ .Reach.State }}" role="{{ if .Reach.OK }}status{{ else }}alert{{ end }}">
<strong>Reachability:</strong> {{ .Reach.Headline }}
{{- if .Reach.Detail }} {{ .Reach.Detail }}{{ end }}
{{- if .Reach.PublicURL }} <span class="muted">({{ .Reach.PublicURL }})</span>{{ end }}
{{- if .Reach.TailnetURL }} <span class="muted">({{ .Reach.TailnetURL }})</span>{{ end }}
{{- if .Reach.Err }} <span class="err">{{ .Reach.Err }}</span>{{ end }}
</div>

<div id="unraid-live" hx-get="/unraid" hx-trigger="every 15s" hx-select="#unraid-live" hx-swap="outerHTML">

<h2>Host</h2>
{{- if .Host.OK }}
<p>{{ .Host.Data.Hostname }} — Unraid {{ .Host.Data.UnraidVersion }} — {{ .Host.Data.CPUModel }} ({{ .Host.Data.CPUCores }} cores)</p>
<p>CPU: <strong>{{ .Host.Data.CPUPercent }}</strong> — Memory: <strong>{{ .Host.Data.MemUsed }}</strong> / {{ .Host.Data.MemTotal }} — Uptime: {{ .Host.Data.Uptime }}</p>
{{- if .Host.Data.Degraded }}
<p class="alert-unauth">Read from the server's own /proc, not the Unraid API — no API key is configured. These are real measurements of this machine, not estimates; only the source differs.</p>
{{- end }}
<p class="muted">Collected at {{ .Host.Data.CollectedAt }}</p>
{{- else }}
<p class="err">{{ .Host.Err }}</p>
{{- end }}

<h2>Array</h2>
{{- if .Array.OK }}
<p>State: <strong>{{ .Array.Data.State }}</strong>
{{- if .Array.Data.ParityActive }} — parity check running: {{ .Array.Data.ParityProgress }}
{{- else }} — last parity check: {{ .Array.Data.ParityLastCheck }} ({{ .Array.Data.ParityLastErrors }} errors)
{{- end }}</p>
{{- if .Array.Data.Degraded }}
<p class="alert-unauth">Read from Unraid's on-disk state files, not the Unraid API — no API key is configured, so some fields the API would provide are unavailable here. Values shown are real; absent ones are simply not in this source.</p>
{{- end }}
<table>
<thead><tr><th>Disk</th><th>Role</th><th>Size</th><th>Used</th><th>Temp</th><th>SMART</th></tr></thead>
<tbody>
{{- range .Array.Data.Disks }}
<tr><td>{{ .Name }}</td><td>{{ .Role }}</td><td>{{ .Size }}</td><td>{{ .Used }}</td><td{{ if .Standby }} class="disk-standby"{{ end }}>{{ .Temp }}</td><td>{{ .SmartStatus }}</td></tr>
{{- end }}
</tbody>
</table>
{{- else }}
<p class="err">{{ .Array.Err }}</p>
{{- end }}

<h2>Shares</h2>
{{- if .Shares.OK }}
<table>
<thead><tr><th>Share</th><th>Size</th><th>Used</th><th>Free</th><th>Allocator</th><th>Cache pool</th><th>Exported</th><th>Accessible</th></tr></thead>
<tbody>
{{- range .Shares.Data }}
<tr><td>{{ .Name }}</td><td>{{ .Size }}</td><td>{{ .Used }}</td><td>{{ .Free }}</td><td>{{ .Allocator }}</td><td>{{ .CachePool }}</td><td>{{ .Exported }}</td><td>{{ .Accessible }}</td></tr>
{{- end }}
</tbody>
</table>
{{- else }}
<p class="err">{{ .Shares.Err }}</p>
{{- end }}

<h2>Containers</h2>
{{- if .Containers.OK }}
<table>
<thead><tr><th>Container</th><th>Image</th><th>State</th><th>Status</th><th>Ports</th><th>Autostart</th></tr></thead>
<tbody>
{{- range .Containers.Data }}
<tr><td>{{ .Name }}</td><td>{{ .Image }}</td><td>{{ .State }}</td><td>{{ .Status }}</td><td>{{ .Ports }}</td><td>{{ .AutoRun }}</td></tr>
{{- end }}
</tbody>
</table>
{{- else }}
<p class="err">{{ .Containers.Err }}</p>
{{- end }}

{{- if .Proposals }}
<h2>Script proposals</h2>
<p class="muted">A dry run shows what a script would do. It has not been run and no changes have been made.</p>
{{- range .Proposals }}
<h3>{{ .Title }} <small class="muted">({{ .RequestedBy }}, {{ .RequestedAt }})</small></h3>
<pre class="tool-output">{{ .DryRunOutput }}</pre>
{{- if .Pending }}
<form class="inline" method="post" action="/api/unraid/proposals/{{ .ID }}/approve"><button class="btn btn-primary" type="submit">Approve</button></form>
<form class="inline" method="post" action="/api/unraid/proposals/{{ .ID }}/deny"><button class="btn btn-danger" type="submit">Deny</button></form>
{{- else }}
<p>Decision: <strong class="a-{{ .Decision }}">{{ .Decision }}</strong>{{ if .DecidedBy }} by {{ .DecidedBy }}{{ end }} at {{ .DecidedAt }}</p>
{{- end }}
{{- end }}
{{- end }}

</div>
</body>
</html>`))

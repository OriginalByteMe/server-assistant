// commands.go — the operator-initiated command surface (issue #51): the
// dashboard's first UI that mutates anything. Tier IN only: a closed
// catalog of verbs with config-resolved targets, no free-form input ever
// accepted from the client — Run takes the command's ID and nothing else.
// The human's click IS the approval for this tier, which is exactly why the
// catalog must stay closed and config-driven (CommandBackend owns
// internal/config/commands.go) rather than accepting a container name from
// the request body.
//
// CommandSource is deliberately narrow and defined here, not core,
// mirroring ProposalSource (proposals.go): the real catalog/executor is a
// follow-up ticket (CommandBackend). This ticket only needs the seam to
// compile against a fake.
package web

import (
	"context"
	"html/template"
	"net/http"
	"time"

	"server-assistant/internal/core"
)

// commandRunTimeout bounds one operator command run. Deliberately larger
// than handlerTimeout: this handler mutates, and mutations are slow — see
// handleAPICommandRun's comment for why sharing the read budget broke every
// real restart.
const commandRunTimeout = 60 * time.Second

// Command is one operator-runnable action the dashboard can offer. ID is
// stable and opaque to the client (e.g. "restart-container:sa-demo-web");
// Consequence is the one plain-English line a non-expert reads BEFORE
// clicking, since this tier has no second approval gate — the click IS the
// approval.
type Command struct {
	ID          string
	Label       string
	Description string
	Consequence string
}

// CommandResult is what actually happened, never a generic "done"
// (CONVENTIONS rule 5): Output carries the real command output or error
// text, and the timestamps let the panel show real elapsed time.
type CommandResult struct {
	OK         bool
	Output     string
	StartedAt  time.Time
	FinishedAt time.Time
}

// CommandSource is the seam the dashboard's Commands panel renders and acts
// against. Run takes the command's ID ONLY — never a target name or
// argument from the client — and an unknown ID is an error, never a
// pass-through; that closedness is what makes an unauthenticated click a
// safe approval gate (issue #51).
type CommandSource interface {
	Commands(ctx context.Context) ([]Command, error)
	Run(ctx context.Context, id, who string) (CommandResult, error)
}

// commandRow is the template view of one Command, optionally carrying the
// outcome of a just-completed Run. Ran is false on the initial page load —
// the consequence line must be visible before any click (issue #51: no
// second approval gate, so the operator must see what a button does
// without pressing it).
type commandRow struct {
	ID          string
	Label       string
	Description string
	Consequence string
	Ran         bool
	OK          bool
	Output      string
	StartedAt   string
	Duration    string
}

func commandRowOf(c Command, result *CommandResult) commandRow {
	row := commandRow{
		ID:          c.ID,
		Label:       c.Label,
		Description: c.Description,
		Consequence: c.Consequence,
	}
	if result != nil {
		row.Ran = true
		row.OK = result.OK
		row.Output = result.Output
		row.StartedAt = result.StartedAt.Format(time.RFC3339)
		row.Duration = result.FinishedAt.Sub(result.StartedAt).Round(time.Millisecond).String()
	}
	return row
}

// commandsSection is the /unraid template's Commands panel state. A nil
// *commandsSection (no CommandSource wired in) omits the panel entirely —
// the same nil-means-absent convention as Proposals. A non-nil section with
// no rows means the catalog is genuinely empty (the deployed default, per
// issue #51's risk register) and renders an explicit empty state, never a
// blank panel. Err is set instead of Rows when the catalog read itself
// failed (rule 5: never hide it behind an empty list).
type commandsSection struct {
	Err  string
	Rows []commandRow
}

func commandsSectionOf(ctx context.Context, cs CommandSource) *commandsSection {
	if cs == nil {
		return nil
	}
	cmds, err := cs.Commands(ctx)
	if err != nil {
		return &commandsSection{Err: err.Error()}
	}
	rows := make([]commandRow, 0, len(cmds))
	for _, c := range cmds {
		rows = append(rows, commandRowOf(c, nil))
	}
	return &commandsSection{Rows: rows}
}

// HandlerComplete returns the dashboard mux with everything HandlerFull
// wires in, plus — when cs is non-nil — the operator-initiated Commands
// panel and its run route. HandlerFull (unraid.go) keeps working unchanged
// for callers that don't have a CommandSource yet; both delegate to
// buildMux (web.go) so routing lives in exactly one place. cs may be nil
// until the real catalog (CommandBackend) lands, same nil-means-absent
// convention as ps.
func HandlerComplete(vs ViewSource, hs HarnessSource, us core.UnraidSource, ps ProposalSource, cs CommandSource) http.Handler {
	return buildMux(vs, hs, us, ps, cs)
}

// handleAPICommandRun implements POST /api/unraid/commands/{id}/run. Only
// the path's {id} ever reaches CommandSource.Run — no request body, form
// value, or query parameter is read for the target (issue #51: the catalog
// is the only source of truth for what a command does).
//
// A non-htmx caller (e.g. a script, or this handler's own tests) gets the
// real CommandResult as JSON. A request HTMX itself made (HX-Request:
// true, set automatically by the panel's Run button) gets the re-rendered
// row fragment instead, so the outcome swaps into the panel in place — no
// page navigation.
//
// Whether Run fails outright (err != nil, e.g. an unknown ID) or completes
// with CommandResult.OK == false, both render identically as a failed
// outcome: rule 5 forbids a generic "done" standing in for either, and a
// backend error is exactly as real as an application-level failure.
func handleAPICommandRun(cs CommandSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// NOT handlerTimeout: that 5s ceiling is sized for read handlers,
		// and a container restart legitimately takes longer than it (a
		// busybox container ignoring SIGTERM alone burns ~5s before the
		// engine SIGKILLs it). Sharing the read budget here made every
		// real restart fail with "context deadline exceeded" while the
		// restart itself succeeded on the host, and made the configured
		// commands.timeout unreachable — the outer context always won.
		// This is a backstop above CommandSource's own budget (which
		// internal/commands bounds at commands.timeout, default 30s), so
		// no handler can hang forever; a commands.timeout set above 60s
		// is truncated here.
		ctx, cancel := context.WithTimeout(r.Context(), commandRunTimeout)
		defer cancel()

		id := r.PathValue("id")
		who := r.FormValue("who")
		if who == "" {
			who = "operator"
		}

		result, err := cs.Run(ctx, id, who)
		if err != nil {
			now := time.Now()
			result = CommandResult{OK: false, Output: err.Error(), StartedAt: now, FinishedAt: now}
		}

		row := commandRowOf(Command{ID: id}, &result)
		lookupCtx, lookupCancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer lookupCancel()
		if cmds, cerr := cs.Commands(lookupCtx); cerr == nil {
			for _, c := range cmds {
				if c.ID == id {
					row = commandRowOf(c, &result)
					break
				}
			}
		}

		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := commandRowTmpl.Execute(w, row); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		writeJSON(w, http.StatusOK, toCommandResultDTO(result))
	}
}

type commandResultDTO struct {
	OK         bool   `json:"ok"`
	Output     string `json:"output"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

func toCommandResultDTO(r CommandResult) commandResultDTO {
	return commandResultDTO{
		OK:         r.OK,
		Output:     r.Output,
		StartedAt:  r.StartedAt.Format(time.RFC3339),
		FinishedAt: r.FinishedAt.Format(time.RFC3339),
	}
}

// commandRowTmpl is the single source of truth for one command row's
// markup — associated into unraidPageTmpl's (unraid.go) template set as
// "command-row" so the full page's {{range .Commands.Rows}} and this
// handler's HTMX fragment response render byte-identical markup, matching
// cellsTmpl's (web.go) same-markup-two-callers convention.
var commandRowTmpl = template.Must(unraidPageTmpl.New("command-row").Parse(`<div id="cmd-{{ .ID }}" class="command-row">
<h3>{{ .Label }}</h3>
{{- if .Description }}
<p>{{ .Description }}</p>
{{- end }}
<p class="muted"><strong>What this does:</strong> {{ .Consequence }}</p>
{{- if .Ran }}
{{- if .OK }}
<p>Succeeded in {{ .Duration }} (started {{ .StartedAt }})</p>
{{- else }}
<p class="err">Failed after {{ .Duration }} (started {{ .StartedAt }})</p>
{{- end }}
<pre class="tool-output">{{ .Output }}</pre>
{{- end }}
<button class="btn btn-primary" type="button" hx-post="/api/unraid/commands/{{ .ID }}/run" hx-target="#cmd-{{ .ID }}" hx-swap="outerHTML">Run</button>
</div>`))

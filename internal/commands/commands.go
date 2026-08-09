// Package commands implements HL-SA-21's closed operator-command catalog:
// the dashboard's "IN" action tier (issue #51) — a closed verb
// (restart-container) whose target is resolved entirely from config, never
// from the request. The human clicking the button in the dashboard IS the
// approval for this tier; there is no second gate, which is exactly why the
// catalog stays config-driven and closed rather than accepting a container
// name from the caller.
//
// This package is dashboard-only and human-initiated. It must never be
// wired as a mutating MCP tool: an LLM-initiated mutation goes through the
// existing propose/dry-run/approve flow in internal/scripts instead.
//
// Source defines its own Command/CommandResult types rather than importing
// internal/web's identically-shaped web.CommandSource contract types, so
// this package stays free of any internal/web dependency — the composition
// root (cmd/server-assistant/main.go) adapts between the two.
package commands

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"server-assistant/internal/config"
	"server-assistant/internal/unraid"
)

// restartVerb is the one closed verb this catalog currently exposes.
const restartVerb = "restart-container"

// ErrUnknownCommand is returned by Run for any id that does not name a
// currently-allowlisted restart target: malformed, naming an unknown verb,
// or naming a container that was removed from the allowlist since the
// dashboard rendered the button. Wrapped, never swallowed, so callers can
// errors.Is check it.
var ErrUnknownCommand = errors.New("commands: unknown or disallowed command id")

// Command is one catalog entry offered to a human operator.
type Command struct {
	ID          string
	Label       string
	Description string
	Consequence string
}

// CommandResult is the outcome of one Run.
type CommandResult struct {
	OK         bool
	Output     string
	StartedAt  time.Time
	FinishedAt time.Time
}

// Source is the closed operator-command catalog and executor. Safe for
// concurrent use: cfg is immutable after config.Load, docker is an
// *http.Client-backed value safe for concurrent use by design (mirrors
// unraid.Source's own reasoning), and log is a *slog.Logger.
type Source struct {
	cfg    config.CommandsConfig
	docker *unraid.DockerClient
	log    *slog.Logger
}

// New builds a Source. docker is the same Docker-Engine-over-Unix-socket
// client type internal/unraid.Source uses — construct it the same way
// (unraid.NewDockerClient(cfg.Unraid.DockerSocket)) rather than writing a
// second client. log must not be nil.
func New(cfg config.CommandsConfig, docker *unraid.DockerClient, log *slog.Logger) *Source {
	return &Source{cfg: cfg, docker: docker, log: log}
}

// Commands lists one restart-container Command per name in the config
// allowlist. An empty (default) allowlist yields an empty catalog — no
// runnable commands at all until an operator opts a container in.
func (s *Source) Commands(_ context.Context) ([]Command, error) {
	cmds := make([]Command, 0, len(s.cfg.AllowRestart))
	for _, name := range s.cfg.AllowRestart {
		cmds = append(cmds, Command{
			ID:          restartVerb + ":" + name,
			Label:       "Restart " + name,
			Description: "Restart the " + name + " container via the Docker Engine API.",
			Consequence: fmt.Sprintf("Stops and starts the %s container. Anything using it will drop its connections for a few seconds.", name),
		})
	}
	return cmds, nil
}

// Run executes id on behalf of who (an operator identity string threaded
// through for the audit record only — this tier has no further
// authorization of its own; see the package doc). id is parsed and
// re-validated against the CURRENT config allowlist before anything reaches
// Docker: the id round-tripped through the browser is never trusted on its
// own, so an id whose target has since been removed from the allowlist — or
// that never named an allowlisted target at all — is refused without
// issuing a single Docker call.
func (s *Source) Run(ctx context.Context, id, who string) (CommandResult, error) {
	name, err := s.resolveTarget(id)
	if err != nil {
		s.audit(id, who, "", false, err.Error(), time.Time{}, time.Time{})
		return CommandResult{}, err
	}

	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout())
	defer cancel()

	restartErr := s.docker.Restart(runCtx, name)
	finished := time.Now()

	if restartErr != nil {
		output := restartErr.Error()
		s.audit(id, who, name, false, output, started, finished)
		return CommandResult{OK: false, Output: output, StartedAt: started, FinishedAt: finished},
			fmt.Errorf("commands: run %s: %w", id, restartErr)
	}

	output := fmt.Sprintf("%s restarted", name)
	s.audit(id, who, name, true, output, started, finished)
	return CommandResult{OK: true, Output: output, StartedAt: started, FinishedAt: finished}, nil
}

// resolveTarget parses id as "restart-container:<name>" and checks name
// against the current allowlist. Any failure returns ErrUnknownCommand
// wrapped with the reason, before any Docker call is issued.
func (s *Source) resolveTarget(id string) (string, error) {
	verb, name, ok := strings.Cut(id, ":")
	if !ok || verb != restartVerb || name == "" {
		return "", fmt.Errorf("%w: %q", ErrUnknownCommand, id)
	}
	for _, allowed := range s.cfg.AllowRestart {
		if allowed == name {
			return name, nil
		}
	}
	return "", fmt.Errorf("%w: %q not in commands.allow_restart", ErrUnknownCommand, id)
}

// audit records one attempt — including refusals — via log/slog at INFO
// with a stable structured shape. internal/scripts.Store.AppendAudit binds
// to a script Proposal's content-hash identity and its own SQLite table
// (internal/store/migrations/00006_scripts.sql); a restart-container run
// has neither a proposal nor a script hash, so bolting it onto that schema
// would distort the schema rather than reuse it. This is a deliberate,
// narrower audit trail for the closed-verb tier, not a missing one.
func (s *Source) audit(id, who, target string, ok bool, output string, started, finished time.Time) {
	s.log.Info("command run",
		"id", id,
		"who", who,
		"target", target,
		"ok", ok,
		"output", output,
		"started_at", started,
		"finished_at", finished,
	)
}

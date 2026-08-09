package config

import (
	"fmt"
	"time"
)

// CommandsConfig bounds HL-SA-21's closed operator-command catalog: the
// dashboard's "IN" action tier (issue #51) — a closed verb
// (restart-container) with a target resolved entirely from config, never
// from the request. The human clicking the button in the dashboard IS the
// approval for this tier; there is no second gate, which is exactly why the
// catalog must stay config-driven and closed rather than accepting a
// container name from the request body.
//
// AllowRestart defaults to an EMPTY list. This is deliberate, not an
// oversight: the dashboard is currently UNAUTHENTICATED (Noah's standing
// decision), and the project's risk register says plainly — "Do not widen
// allow_restart beyond the demo container until auth lands — that is the
// moment unauthenticated mutation of real services becomes one POST away."
// An operator must opt a container in by editing this file; nothing in
// this package will ever default one in.
type CommandsConfig struct {
	// AllowRestart lists the container names the restart-container command
	// may target. Re-checked at Run time (internal/commands.Source.Run),
	// not just at catalog-build time — removing a name from this list must
	// refuse an in-flight id even if the dashboard already rendered the
	// button for it.
	AllowRestart []string `yaml:"allow_restart"`
	// TimeoutStr bounds one restart call against the Docker Engine API
	// (CONVENTIONS rule 4). Defaults to 30s.
	TimeoutStr string `yaml:"timeout"`

	timeout time.Duration // resolved by resolve()
}

// Timeout is the resolved per-run deadline.
func (c CommandsConfig) Timeout() time.Duration { return c.timeout }

// resolve parses TimeoutStr and validates every allowlisted name against
// the same safe character set a Service/Host name must satisfy
// (containerNameRe) — the name is interpolated straight into a Docker API
// path segment, so anything outside [A-Za-z0-9_.-] is rejected at load
// (rule 6), never discovered at run time.
func (c *CommandsConfig) resolve() error {
	var err error
	if c.timeout, err = parseDurationDefault(c.TimeoutStr, 30*time.Second); err != nil {
		return fmt.Errorf("commands.timeout: %w", err)
	}
	seen := map[string]struct{}{}
	deduped := make([]string, 0, len(c.AllowRestart))
	for _, name := range c.AllowRestart {
		if !containerNameRe.MatchString(name) {
			return fmt.Errorf("commands.allow_restart: invalid container name %q", name)
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		deduped = append(deduped, name)
	}
	c.AllowRestart = deduped
	return nil
}

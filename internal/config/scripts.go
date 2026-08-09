package config

import (
	"fmt"
	"time"
)

// ScriptsConfig bounds the HL-SA-18 script proposal/dry-run/grant subsystem
// (issue #51/#55). Durations are Go duration strings parsed here
// (CONVENTIONS rule 3, consistent with History/Host/Service — not
// Harness's documented one-off exception), not native yaml time.Duration
// decoding. Not a pointer, same as History/Sampler: always present with
// defaults so grant TTLs are never silently unset.
type ScriptsConfig struct {
	DryRunTimeoutStr    string `yaml:"dry_run_timeout"`
	RunTimeoutStr       string `yaml:"run_timeout"`
	SessionGrantTTLStr  string `yaml:"session_grant_ttl"`
	StandingGrantTTLStr string `yaml:"standing_grant_ttl"`
	// ProtectedPaths are bind-mounted read-only for every real script
	// execution (issue #51: scripts may never write /boot). Defaults to
	// {"/boot"} when omitted — never to an empty list, which would silently
	// disable the ban.
	ProtectedPaths []string `yaml:"protected_paths"`

	dryRunTimeout    time.Duration
	runTimeout       time.Duration
	sessionGrantTTL  time.Duration
	standingGrantTTL time.Duration
}

func (s ScriptsConfig) DryRunTimeout() time.Duration    { return s.dryRunTimeout }
func (s ScriptsConfig) RunTimeout() time.Duration       { return s.runTimeout }
func (s ScriptsConfig) SessionGrantTTL() time.Duration  { return s.sessionGrantTTL }
func (s ScriptsConfig) StandingGrantTTL() time.Duration { return s.standingGrantTTL }

// ProtectedFSPaths returns the configured protected paths, defaulting to
// {"/boot"} when the operator did not override it.
func (s ScriptsConfig) ProtectedFSPaths() []string {
	if len(s.ProtectedPaths) == 0 {
		return []string{"/boot"}
	}
	return s.ProtectedPaths
}

// resolve parses ScriptsConfig's duration strings, applying defaults for
// every omitted knob (coordinator decision C5, GitHub #55: session 4h,
// standing 90d).
func (s *ScriptsConfig) resolve() error {
	var err error
	if s.dryRunTimeout, err = parseDurationDefault(s.DryRunTimeoutStr, 15*time.Second); err != nil {
		return fmt.Errorf("scripts.dry_run_timeout: %w", err)
	}
	if s.runTimeout, err = parseDurationDefault(s.RunTimeoutStr, 5*time.Minute); err != nil {
		return fmt.Errorf("scripts.run_timeout: %w", err)
	}
	if s.sessionGrantTTL, err = parseDurationDefault(s.SessionGrantTTLStr, 4*time.Hour); err != nil {
		return fmt.Errorf("scripts.session_grant_ttl: %w", err)
	}
	if s.standingGrantTTL, err = parseDurationDefault(s.StandingGrantTTLStr, 90*24*time.Hour); err != nil {
		return fmt.Errorf("scripts.standing_grant_ttl: %w", err)
	}
	return nil
}

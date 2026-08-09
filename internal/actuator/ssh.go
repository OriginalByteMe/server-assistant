package actuator

// Package actuator dispatches the M2-v1 Action catalog — restart_container
// only (ADR 0011), config may only narrow the allowlist (ADR 0010) — over
// SSH using the separate, catalog-scoped write credential (ADR 0022), never
// the read-only Probe credential. Dispatch success means the command was
// sent and exited zero; it is not a claim of recovery. Only the v1
// monitoring spine's next committed Status adjudicates outcome (ADR 0016) —
// the Actuator never grades its own homework.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"server-assistant/internal/core"
	"server-assistant/internal/prober"
)

// containerNameRe defends against injection via a misconfigured allowlist:
// only this strict charset ever reaches a shell command. The allowlist check
// is defence in depth (ADR 0010/0011), not the only gate.
var containerNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// SSH is the write-credential Actuator (ADR 0022). It restarts only
// containers named in the configured allowlist (ADR 0010/0011: the catalog
// is code, config may only narrow it) and refuses everything else.
type SSH struct {
	runner prober.Runner
	allow  map[string]struct{}
}

var _ core.Actuator = (*SSH)(nil)

// NewSSH returns an Actuator scoped to allow, the restart_container
// allowlist. r carries the separate write credential (ADR 0022) — wiring
// which credential that is is the composition root's job, not this
// package's.
func NewSSH(r prober.Runner, allow []string) *SSH {
	set := make(map[string]struct{}, len(allow))
	for _, c := range allow {
		set[c] = struct{}{}
	}
	return &SSH{runner: r, allow: set}
}

// RestartContainer dispatches `docker restart <container>` over the write
// credential. Success means the command was dispatched and exited zero — it
// is NOT a claim of recovery (ADR 0016): only the v1 spine's next committed
// Status decides whether the incident actually recovered.
func (s *SSH) RestartContainer(ctx context.Context, container string) error {
	if !containerNameRe.MatchString(container) {
		return fmt.Errorf("actuator: refusing invalid container name %q", container)
	}
	if _, ok := s.allow[container]; !ok {
		return fmt.Errorf("actuator: container %q is not in the restart allowlist", container)
	}
	if _, err := s.runner.Run(ctx, "docker restart "+container); err != nil {
		return fmt.Errorf("actuator: restart %s: %w", container, err)
	}
	return nil
}

// Healthy runs a read-only no-op with the write credential, for harness
// self-monitoring (ADR 0015: the harness monitors itself like any other
// subject). It must not mutate anything.
func (s *SSH) Healthy(ctx context.Context) error {
	out, err := s.runner.Run(ctx, "docker version --format {{.Server.Version}}")
	if err != nil {
		return fmt.Errorf("actuator: health check: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("actuator: health check: empty output")
	}
	return nil
}

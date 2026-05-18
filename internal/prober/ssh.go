package prober

import (
	"context"
	"fmt"
	"strings"

	"server-assistant/internal/core"
)

// Runner runs one bounded, read-only command on the Host over SSH and returns
// its stdout. It is the seam (ADR 0006 / CONVENTIONS rule 2) that keeps the
// SSH probers' derivation logic network-free and unit-testable (rule 9): the
// real implementation is *SSHClient; tests use a canned fake.
type Runner interface {
	Run(ctx context.Context, cmd string) (string, error)
}

// ContainerProbe reads a Service's container state over SSH and derives the
// Service's Status (CONTEXT.md: a Service is healthy when it does its job,
// which a container being "running" is a proxy for). It feeds the same
// debounce → commit → dashboard → Alert pipeline as every other Prober.
//
// An SSH/command failure is NOT a measurement of the container — returning
// DOWN would let an SSH blip commit a false outage and fire a spurious Alert.
// The observer never collapses "can't tell" into "down" (rule 5 / ADR 0005):
// the error is surfaced so the monitor skips the probe (never commits DOWN).
type ContainerProbe struct {
	name      string
	runner    Runner
	container string
}

var _ core.Prober = (*ContainerProbe)(nil)

// NewContainerProbe returns a Prober that reports Status from the named
// container's runtime state, read over the shared SSH Runner.
func NewContainerProbe(name string, r Runner, container string) *ContainerProbe {
	return &ContainerProbe{name: name, runner: r, container: container}
}

func (p *ContainerProbe) Name() string { return p.name }

// containerStateCmd reads exactly the two fields we derive Status from, in one
// read-only call. Health is empty for containers without a HEALTHCHECK — that
// is treated as "no opinion", i.e. running ⇒ UP.
func containerStateCmd(container string) string {
	return fmt.Sprintf(
		`docker inspect -f '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' %s`,
		container)
}

func (p *ContainerProbe) Probe(ctx context.Context) (core.ProbeResult, error) {
	out, err := p.runner.Run(ctx, containerStateCmd(p.container))
	if err != nil {
		// Can't tell — never DOWN (rule 5 / ADR 0005). Monitor skips on error.
		return core.ProbeResult{}, fmt.Errorf("container probe %s: %w", p.name, err)
	}

	state, health, _ := strings.Cut(strings.TrimSpace(out), "|")
	switch state {
	case "running":
		// A failing/initialising healthcheck is "reachable but not doing its
		// job well" — DEGRADED, never silently UP and never DOWN.
		if health == "unhealthy" || health == "starting" {
			return core.ProbeResult{Status: core.StatusDegraded}, nil
		}
		return core.ProbeResult{Status: core.StatusUp}, nil
	case "exited", "restarting", "created", "dead", "paused", "removing":
		return core.ProbeResult{
			Status: core.StatusDown,
			Err:    fmt.Errorf("%s container %s is %s", p.name, p.container, state),
		}, nil
	default:
		// Unrecognised/empty output is not proof the Service is dead — can't
		// tell, so surface an error rather than collapse to DOWN (rule 5).
		return core.ProbeResult{}, fmt.Errorf("container probe %s: unparseable state %q", p.name, out)
	}
}

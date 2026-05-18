package prober

import (
	"context"
	"fmt"
	"strconv"
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

// HostMetricsProbe reads one structured key=value report over SSH and derives
// the Host's Status from array state, disk/parity health, and CPU/RAM
// pressure. It is a core.Prober, so it drives Host Status through the same
// debounce → commit → dashboard → Alert pipeline (and ARK-12's gate) as any
// other subject. The remote command is read-only and bounded.
//
// Derivation (v1): array not STARTED ⇒ DOWN (the Host is not doing its job);
// any disabled/invalid disk ⇒ DEGRADED (redundancy compromised); sustained
// load (>2× CPUs) or <5% free memory ⇒ DEGRADED; otherwise UP. A failure or a
// report missing the critical array field is "can't tell" — an error, never
// DOWN (rule 5 / ADR 0005); ARK-12's gate owns UNKNOWN.
type HostMetricsProbe struct {
	name   string
	runner Runner
}

var _ core.Prober = (*HostMetricsProbe)(nil)

func NewHostMetricsProbe(name string, r Runner) *HostMetricsProbe {
	return &HostMetricsProbe{name: name, runner: r}
}

func (p *HostMetricsProbe) Name() string { return p.name }

// hostMetricsCmd emits a fixed, parseable key=value report in one read-only
// call: array state + disk/parity counters from mdcmd, load from
// /proc/loadavg, CPU count, and memory from /proc/meminfo. Tests feed canned
// output through the Runner seam (rule 9); the remote command's behaviour on
// real Unraid is the external verification this issue calls out.
const hostMetricsCmd = `mdcmd status 2>/dev/null | grep -E '^(mdState|mdNumDisabled|mdNumInvalid)=' ; ` +
	`echo "load1=$(cut -d' ' -f1 /proc/loadavg)" ; ` +
	`echo "cpus=$(nproc)" ; ` +
	`awk '/^MemTotal:/{print "memTotal="$2} /^MemAvailable:/{print "memAvailable="$2}' /proc/meminfo`

func (p *HostMetricsProbe) Probe(ctx context.Context) (core.ProbeResult, error) {
	out, err := p.runner.Run(ctx, hostMetricsCmd)
	if err != nil {
		return core.ProbeResult{}, fmt.Errorf("host-metrics probe %s: %w", p.name, err)
	}

	kv := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = strings.TrimSpace(v)
		}
	}

	// mdState is the one field we cannot derive Host health without. Its
	// absence is "can't tell", never DOWN (rule 5 / ADR 0005).
	state, ok := kv["mdState"]
	if !ok {
		return core.ProbeResult{}, fmt.Errorf("host-metrics probe %s: report missing mdState: %q", p.name, out)
	}
	if state != "STARTED" {
		return core.ProbeResult{
			Status: core.StatusDown,
			Err:    fmt.Errorf("%s array mdState=%s (not STARTED)", p.name, state),
		}, nil
	}

	if atoiDefault(kv["mdNumDisabled"], 0) > 0 || atoiDefault(kv["mdNumInvalid"], 0) > 0 {
		return core.ProbeResult{Status: core.StatusDegraded}, nil
	}

	// Sustained overload: load1 above 2× the core count.
	load1 := atofDefault(kv["load1"], 0)
	cpus := atofDefault(kv["cpus"], 1)
	if cpus > 0 && load1 > 2*cpus {
		return core.ProbeResult{Status: core.StatusDegraded}, nil
	}

	// Memory pressure: under 5% available.
	memTotal := atofDefault(kv["memTotal"], 0)
	memAvail := atofDefault(kv["memAvailable"], 0)
	if memTotal > 0 && memAvail/memTotal < 0.05 {
		return core.ProbeResult{Status: core.StatusDegraded}, nil
	}

	return core.ProbeResult{Status: core.StatusUp}, nil
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func atofDefault(s string, def float64) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return f
	}
	return def
}

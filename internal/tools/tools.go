package tools

// Package tools implements the M2-v1 read-only Diagnosis tool surface. The
// tool set is code-defined; config may only narrow it via targets/limits
// (ADR 0021). Every tool resolves the LLM's domain-level "service" argument
// to an implementation container deterministically from reviewed config —
// the LLM never names a container or path itself (ADR 0018) — and the
// resolved name is validated against a strict charset before it ever reaches
// a shell command, defence in depth alongside the read-only SSH credential
// ceiling (ADR 0022).

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"server-assistant/internal/core"
	"server-assistant/internal/prober"
)

// containerNameRe is the same strict allow-charset used at the write path
// (internal/actuator): defence against injection via a misconfigured
// service->container mapping.
var containerNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// clamp bounds v to [1, max], substituting def when v is not positive. Used
// for every tool's config-narrowable numeric knob (ADR 0021 layer 3).
func clamp(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// resolveContainer maps the Diagnosis's domain-level "service" arg to a
// reviewed container name (ADR 0018) and validates the result. An unknown
// service or an invalid resolved name is an error — never a raw or
// unvalidated container name reaching the caller.
func resolveContainer(targets map[string]string, args map[string]string) (string, error) {
	service := args["service"]
	if service == "" {
		return "", fmt.Errorf("missing required arg %q", "service")
	}
	container, ok := targets[service]
	if !ok {
		return "", fmt.Errorf("unknown service %q", service)
	}
	if !containerNameRe.MatchString(container) {
		return "", fmt.Errorf("service %q resolves to invalid container name %q", service, container)
	}
	return container, nil
}

// containerStatusCmd mirrors internal/prober's containerStateCmd: the same
// one-call, read-only docker inspect state/health read.
func containerStatusCmd(container string) string {
	return fmt.Sprintf(
		`docker inspect -f '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' %s`,
		container)
}

type containerStatusTool struct {
	runner  prober.Runner
	targets map[string]string
}

// ContainerStatus reports a Service's container running state and health.
func ContainerStatus(r prober.Runner, targets map[string]string) core.ReadTool {
	return &containerStatusTool{runner: r, targets: targets}
}

var _ core.ReadTool = (*containerStatusTool)(nil)

func (t *containerStatusTool) Name() string { return "container_status" }

func (t *containerStatusTool) Description() string {
	return `Reports a Service's container state and health as "state=<x> health=<y>". Arg: service.`
}

func (t *containerStatusTool) Call(ctx context.Context, args map[string]string) (string, error) {
	container, err := resolveContainer(t.targets, args)
	if err != nil {
		return "", fmt.Errorf("container_status: %w", err)
	}
	out, err := t.runner.Run(ctx, containerStatusCmd(container))
	if err != nil {
		return "", fmt.Errorf("container_status: %w", err)
	}
	// Same "status|health" format as ContainerProbe; a missing separator is
	// truncated/garbled output, not a value the Reasoner should trust.
	state, health, ok := strings.Cut(strings.TrimSpace(out), "|")
	if !ok {
		return "", fmt.Errorf("container_status: malformed output (no status|health separator): %q", out)
	}
	return fmt.Sprintf("state=%s health=%s", state, health), nil
}

type containerLogsTool struct {
	runner  prober.Runner
	targets map[string]string
	lines   int
}

// ContainerLogs tails a Service's container logs. lines is clamped to
// [1, 200] at construction, defaulting to 50 when <= 0.
func ContainerLogs(r prober.Runner, targets map[string]string, lines int) core.ReadTool {
	return &containerLogsTool{runner: r, targets: targets, lines: clamp(lines, 50, 200)}
}

var _ core.ReadTool = (*containerLogsTool)(nil)

func (t *containerLogsTool) Name() string { return "container_logs" }

func (t *containerLogsTool) Description() string {
	return "Tails a Service's container logs (stdout+stderr, bounded lines). Arg: service."
}

func (t *containerLogsTool) Call(ctx context.Context, args map[string]string) (string, error) {
	container, err := resolveContainer(t.targets, args)
	if err != nil {
		return "", fmt.Errorf("container_logs: %w", err)
	}
	cmd := fmt.Sprintf("docker logs --tail %d %s 2>&1", t.lines, container)
	out, err := t.runner.Run(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("container_logs: %w", err)
	}
	return out, nil
}

type statusHistoryTool struct {
	store core.Store
	limit int
}

// StatusHistory reports a Service's recent committed Probe samples from the
// Store already fed by the v1 spine (ADR 0021: reuse, never a new read
// path). limit is clamped to [1, 100] at construction, defaulting to 20.
func StatusHistory(s core.Store, limit int) core.ReadTool {
	return &statusHistoryTool{store: s, limit: clamp(limit, 20, 100)}
}

var _ core.ReadTool = (*statusHistoryTool)(nil)

func (t *statusHistoryTool) Name() string { return "status_history" }

func (t *statusHistoryTool) Description() string {
	return `Reports recent Probe samples for a Service as "<time> <STATUS> <latency>" lines. Arg: service.`
}

func (t *statusHistoryTool) Call(ctx context.Context, args map[string]string) (string, error) {
	service := args["service"]
	if service == "" {
		return "", fmt.Errorf("status_history: missing required arg %q", "service")
	}
	samples, err := t.store.LoadProbeSamples(ctx, service, t.limit)
	if err != nil {
		return "", fmt.Errorf("status_history: %w", err)
	}
	var b strings.Builder
	for i, s := range samples {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s %s %s", s.At.Format(time.RFC3339), s.Status.String(), s.Latency)
	}
	return b.String(), nil
}

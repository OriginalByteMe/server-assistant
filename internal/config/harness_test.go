package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// The harness block ships default-off (ADR 0014): a minimal enabled section
// still gets every optional knob defaulted (rule 6).
func TestLoad_HarnessDefaultsApplied(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n"+
		"services:\n  - name: demo-web\n    url: \"https://example.test\"\n"+
		"harness:\n  mode: shadow\n  reasoner:\n    base_url: \"http://127.0.0.1:11434/v1\"\n    model: \"qwen2.5:1.5b-instruct\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, c.Harness)
	require.Equal(t, 60*time.Second, c.Harness.Reasoner.Timeout)
	require.Equal(t, 4, c.Harness.Ceilings.MaxToolCalls)
	require.Equal(t, 120*time.Second, c.Harness.Ceilings.WallClock)
	require.Equal(t, 10*time.Minute, c.Harness.ApprovalTimeout)
	require.Equal(t, 15*time.Minute, c.Harness.Cooldown)
	require.Equal(t, 3*time.Minute, c.Harness.OutcomeWindow)
	require.Equal(t, 50, c.Harness.LogLines)
}

// An absent harness: section resolves to a non-nil pointer with Mode off —
// Config.Harness is never nil after Load (ADR 0014).
func TestLoad_HarnessAbsentResolvesToOff(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, c.Harness)
	mode, err := core.ParseHarnessMode(c.Harness.Mode)
	require.NoError(t, err)
	require.Equal(t, core.HarnessOff, mode)
}

func TestLoad_HarnessRejectsUnknownMode(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nharness:\n  mode: bogus\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "harness")
}

// ADR 0013 egress gate: a non-loopback base_url without cloud: true is
// rejected — Diagnosis evidence never leaves the box by accident.
func TestLoad_HarnessRejectsNonLoopbackWithoutCloud(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n"+
		"harness:\n  mode: shadow\n  reasoner:\n    base_url: \"http://192.168.1.5:11434/v1\"\n    model: \"qwen2.5:1.5b-instruct\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "0013")
}

// The converse of the egress gate: cloud: true is only meaningful for an
// off-box backend, so a loopback base_url with cloud: true is also rejected.
func TestLoad_HarnessRejectsLoopbackWithCloud(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n"+
		"harness:\n  mode: shadow\n  reasoner:\n    base_url: \"http://127.0.0.1:11434/v1\"\n    model: \"qwen2.5:1.5b-instruct\"\n    cloud: true\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "0013")
}

// A targets key must name a configured Service (ADR 0018 resolution) — the
// Reasoner only ever proposes a Service, never a bare container.
func TestLoad_HarnessRejectsTargetUnknownService(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n"+
		"harness:\n  mode: shadow\n  reasoner:\n    base_url: \"http://127.0.0.1:11434/v1\"\n    model: \"qwen2.5:1.5b-instruct\"\n"+
		"  targets:\n    ghost: sa-demo-web\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "ghost")
	require.Contains(t, err.Error(), "configured service")
}

func liveHarnessBody(extraHarness, ssh string) string {
	return "schema_version: 1\n" +
		"services:\n  - name: demo-web\n    url: \"https://example.test\"\n" +
		ssh +
		"harness:\n  mode: live\n  reasoner:\n    base_url: \"http://127.0.0.1:11434/v1\"\n    model: \"qwen2.5:1.5b-instruct\"\n" +
		"  targets:\n    demo-web: sa-demo-web\n" +
		extraHarness
}

// Live mode requires the scoped write credential (ADR 0022) — Diagnosis and
// Approval alone are not enough to actually dispatch an Action.
func TestLoad_HarnessLiveRequiresWriteSSH(t *testing.T) {
	p := writeTemp(t, liveHarnessBody("  allow_restart:\n    - sa-demo-web\n", ""))
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "write_ssh")
}

// The write credential must be distinct from the shared read-only ssh:
// block (ADR 0022) — the write path never reuses the read-only probe
// credential.
func TestLoad_HarnessLiveRejectsWriteSSHSameAsReadSSH(t *testing.T) {
	ssh := "ssh:\n  address: \"10.0.0.2:22\"\n  user: \"monitor\"\n  key_file: \"/etc/server-assistant/read_key\"\n"
	extra := "  allow_restart:\n    - sa-demo-web\n" +
		"  write_ssh:\n    address: \"10.0.0.2:22\"\n    user: \"monitor\"\n    key_file: \"/etc/server-assistant/read_key\"\n"
	p := writeTemp(t, liveHarnessBody(extra, ssh))
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "0022")
}

// ADR 0010: config may only narrow the code-owned Action catalog, never
// widen it — a live-mode target whose container is absent from
// allow_restart is rejected.
func TestLoad_HarnessLiveRejectsTargetMissingFromAllowRestart(t *testing.T) {
	extra := "  allow_restart:\n    - some-other-container\n" +
		"  write_ssh:\n    address: \"10.0.0.2:22\"\n    user: \"writer\"\n    key_file: \"/etc/server-assistant/write_key\"\n"
	p := writeTemp(t, liveHarnessBody(extra, ""))
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "allow_restart")
	require.Contains(t, err.Error(), "0010")
}

// api_key is a secret — expanded from the environment via ${VAR} like every
// other secret-bearing field (rule 7), never committed to the YAML.
func TestLoad_HarnessExpandsReasonerAPIKey(t *testing.T) {
	t.Setenv("SA_TEST_REASONER_KEY", "sk-test123")
	p := writeTemp(t, "schema_version: 1\n"+
		"harness:\n  mode: shadow\n  reasoner:\n    base_url: \"http://127.0.0.1:11434/v1\"\n    model: \"qwen2.5:1.5b-instruct\"\n    api_key: \"${SA_TEST_REASONER_KEY}\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "sk-test123", c.Harness.Reasoner.APIKey)
}

func TestLoad_HarnessRejectsMaxToolCallsOutOfRange(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n"+
		"harness:\n  mode: shadow\n  reasoner:\n    base_url: \"http://127.0.0.1:11434/v1\"\n    model: \"qwen2.5:1.5b-instruct\"\n"+
		"  ceilings:\n    max_tool_calls: 25\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_tool_calls")
}

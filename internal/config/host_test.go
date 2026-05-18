package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The Host block is optional: a config with no host is valid and Host is nil,
// so the daemon wires the bare spine with no reachability gate (ADR 0006 rule
// 2 — gating attaches behind the seam, it does not reshape the core).
func TestLoad_HostAbsentIsValidAndNil(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Nil(t, c.Host)
}

// A Host block parses with sensible defaults for the optional knobs, so a
// minimal "name + address" is enough to enable the ADR 0005 gate.
func TestLoad_HostParsedWithDefaults(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nhost:\n  name: unraid\n  address: \"10.0.0.2:22\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, c.Host)
	require.Equal(t, "unraid", c.Host.Name)
	require.Equal(t, "10.0.0.2:22", c.Host.Address)
	require.Equal(t, 30*time.Second, c.Host.Poll())
	require.Equal(t, 5*time.Second, c.Host.ProbeTimeout())
	require.Equal(t, 3, c.Host.DebounceN)
}

// The reachability target may embed a secret host via ${VAR}, expanded from
// the environment after parsing like every other secret-bearing field
// (CONVENTIONS rule 7).
func TestLoad_HostAddressExpandsEnvSecret(t *testing.T) {
	t.Setenv("SA_UNRAID_ADDR", "192.168.1.50:22")
	p := writeTemp(t, "schema_version: 1\nhost:\n  name: unraid\n  address: \"${SA_UNRAID_ADDR}\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "192.168.1.50:22", c.Host.Address)
}

func TestLoad_HostRejectsMissingAddress(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nhost:\n  name: unraid\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is required")
}

// The Host name is wired into the dashboard exactly like a Service name (it is
// its own subject row); the same characters that break SSE/HTML wiring are
// rejected at load (rule 6).
func TestLoad_HostRejectsUnsafeName(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nhost:\n  name: \"a,b\"\n  address: \"x:22\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsafe for dashboard wiring")
}

// The Host and a Service share the dashboard subject namespace (one row per
// name, one committed-Status key). A collision would make one shadow the
// other — reject it at load rather than ship an ambiguous dashboard.
func TestLoad_HostNameMustNotCollideWithService(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nhost:\n  name: web\n  address: \"x:22\"\nservices:\n  - name: web\n    url: \"https://x.test\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "host name \"web\" collides")
}

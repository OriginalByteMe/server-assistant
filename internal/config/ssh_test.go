package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The ssh block is optional; absent ⇒ nil and no SSH probes are wired.
func TestLoad_SSHAbsentIsNil(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Nil(t, c.SSH)
}

// The ssh block parses with a sane default timeout; the password is a secret
// resolved from the environment (CONVENTIONS rule 7), never the committed
// YAML.
func TestLoad_SSHParsedWithEnvSecret(t *testing.T) {
	t.Setenv("SA_UNRAID_PW", "s3cret")
	p := writeTemp(t, "schema_version: 1\nssh:\n  address: \"10.0.0.2:22\"\n  user: probe\n  password: \"${SA_UNRAID_PW}\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, c.SSH)
	require.Equal(t, "10.0.0.2:22", c.SSH.Address)
	require.Equal(t, "probe", c.SSH.User)
	require.Equal(t, "s3cret", c.SSH.Password)
	require.Equal(t, 10*time.Second, c.SSH.ProbeTimeout()) // default
}

// An ssh block with neither password nor key_file is a misconfiguration: a
// probe with no credential can never connect (rule 6).
func TestLoad_SSHRejectsNoCredential(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nssh:\n  address: \"10.0.0.2:22\"\n  user: probe\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "ssh: password or key_file is required")
}

// A Service can be probed via its container state instead of url/tcp:
// exactly one of url / tcp / container (rule 6 — unambiguous probe kind).
func TestLoad_ContainerServiceParsed(t *testing.T) {
	t.Setenv("SA_PW", "x")
	p := writeTemp(t, "schema_version: 1\nssh:\n  address: \"h:22\"\n  user: probe\n  password: \"${SA_PW}\"\nservices:\n  - name: plex\n    container: plexmediaserver\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "plexmediaserver", c.Services[0].Container)
}

func TestLoad_ServiceRejectsURLAndContainerTogether(t *testing.T) {
	t.Setenv("SA_PW", "x")
	p := writeTemp(t, "schema_version: 1\nssh:\n  address: \"h:22\"\n  user: u\n  password: \"${SA_PW}\"\nservices:\n  - name: plex\n    url: \"https://x.test\"\n    container: plex\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one of url, tcp or container")
}

// A container Service without an ssh block can never be probed — reject at
// load rather than ship a Service that is permanently UNKNOWN (rule 6).
func TestLoad_ContainerServiceRequiresSSHBlock(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: plex\n    container: plex\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "container probe requires an ssh block")
}

// The Host can be driven by SSH host-metrics instead of (or beyond) bare TCP
// reachability; ssh_metrics needs both a host and an ssh block.
func TestLoad_HostSSHMetricsRequiresSSHBlock(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nhost:\n  name: unraid\n  address: \"h:22\"\n  ssh_metrics: true\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "host ssh_metrics requires an ssh block")
}

func TestLoad_HostSSHMetricsValidWithSSHBlock(t *testing.T) {
	t.Setenv("SA_PW", "x")
	p := writeTemp(t, "schema_version: 1\nssh:\n  address: \"h:22\"\n  user: u\n  password: \"${SA_PW}\"\nhost:\n  name: unraid\n  address: \"h:22\"\n  ssh_metrics: true\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.True(t, c.Host.SSHMetrics)
}

package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoad_UnraidHostProcDefaults(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nunraid:\n  graphql_url: \"http://127.0.0.1/graphql\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, c.Unraid)
	require.Equal(t, "/host/proc", c.Unraid.HostProcPath, "host_proc_path must default to the bind-mounted host procfs path, never the container's own /proc")
	require.Equal(t, 250*time.Millisecond, c.Unraid.CPUSampleInterval())
}

func TestLoad_UnraidHostProcExplicit(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nunraid:\n  graphql_url: \"http://127.0.0.1/graphql\"\n  host_proc_path: \"/custom/proc\"\n  cpu_sample_interval: \"500ms\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, c.Unraid)
	require.Equal(t, "/custom/proc", c.Unraid.HostProcPath)
	require.Equal(t, 500*time.Millisecond, c.Unraid.CPUSampleInterval())
}

package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestLoad_Valid(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nhttp_addr: \":9000\"\ndatabase:\n  path: \"x.db\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, c.SchemaVersion)
	require.Equal(t, ":9000", c.HTTPAddr)
	require.Equal(t, "x.db", c.Database.Path)
}

func TestLoad_DefaultsApplied(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, ":8080", c.HTTPAddr)
	require.Equal(t, "server-assistant.db", c.Database.Path)
}

func TestLoad_RejectsMissingVersion(t *testing.T) {
	p := writeTemp(t, "http_addr: \":8080\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "schema_version is required")
}

func TestLoad_RejectsUnsupportedVersion(t *testing.T) {
	p := writeTemp(t, "schema_version: 99\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported schema_version")
}

func TestLoad_ExpandsEnvSecret(t *testing.T) {
	t.Setenv("SA_TEST_SECRET", "topsecret")
	p := writeTemp(t, "schema_version: 1\ndatabase:\n  path: \"${SA_TEST_SECRET}.db\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "topsecret.db", c.Database.Path)
}

func TestLoad_RejectsUnsetEnvReference(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\ndatabase:\n  path: \"${SA_DEFINITELY_UNSET_VAR}.db\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unset environment variables")
	require.Contains(t, err.Error(), "SA_DEFINITELY_UNSET_VAR")
}

func TestLoad_ServiceParsedWithDefaults(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: web\n    url: \"https://example.test\"\n    latency_threshold: \"750ms\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Len(t, c.Services, 1)
	s := c.Services[0]
	require.Equal(t, "web", s.Name)
	require.Equal(t, 750*time.Millisecond, s.Threshold())
	require.Equal(t, 30*time.Second, s.Poll())         // default
	require.Equal(t, 10*time.Second, s.ProbeTimeout()) // default
	require.Equal(t, 3, s.DebounceN)                   // default
}

func TestLoad_RejectsServiceWithoutURL(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: web\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "url is required")
}

func TestLoad_RejectsBadDuration(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: web\n    url: \"https://x.test\"\n    poll_interval: \"notaduration\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "poll_interval")
}

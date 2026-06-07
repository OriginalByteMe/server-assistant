package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A Service can be configured with a TCP/port probe instead of a URL:
// `tcp: host:port`, with the same optional knobs and defaults as an HTTP
// Service. This is how non-HTTP Services (game servers, databases) join the
// same Status pipeline (ARK-8).
func TestLoad_TCPServiceParsedWithDefaults(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: gameserver\n    tcp: \"10.0.0.5:25565\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Len(t, c.Services, 1)
	s := c.Services[0]
	require.Equal(t, "gameserver", s.Name)
	require.Equal(t, "10.0.0.5:25565", s.TCPAddr)
	require.Equal(t, "", s.URL)
	require.Equal(t, 30*time.Second, s.Poll())         // default
	require.Equal(t, 10*time.Second, s.ProbeTimeout()) // default
	require.Equal(t, 3, s.DebounceN)                   // default
}

// A Service is either HTTP or TCP, never both and never neither: the probe
// kind must be unambiguous (rule 6 — config is the source of truth).
func TestLoad_ServiceRejectsBothURLAndTCP(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: x\n    url: \"https://x.test\"\n    tcp: \"x:22\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one of url or tcp")
}

func TestLoad_ServiceRejectsNeitherURLNorTCP(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: x\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one of url or tcp")
}

// The TCP target may embed a secret host via ${VAR}, expanded from the
// environment after parsing like every other secret-bearing field (rule 7).
func TestLoad_TCPAddrExpandsEnvSecret(t *testing.T) {
	t.Setenv("SA_GAME_ADDR", "192.168.1.9:25565")
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: game\n    tcp: \"${SA_GAME_ADDR}\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "192.168.1.9:25565", c.Services[0].TCPAddr)
}

// Regression: an HTTP (url) Service still parses exactly as before — the xor
// rule must not break the existing HTTP vertical.
func TestLoad_HTTPServiceStillValid(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: web\n    url: \"https://example.test\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "https://example.test", c.Services[0].URL)
	require.Equal(t, "", c.Services[0].TCPAddr)
}

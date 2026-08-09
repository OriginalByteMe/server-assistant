package commands

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/config"
	"server-assistant/internal/unraid"
)

// loadCommandsConfig loads a minimal valid Config from body and returns its
// resolved Commands section — the private duration fields (e.g. Timeout())
// are only populated by Config.validate(), so tests go through the real
// loader rather than constructing config.CommandsConfig by hand.
func loadCommandsConfig(t *testing.T, body string) config.CommandsConfig {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	c, err := config.NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	return c.Commands
}

// fakeDockerServer serves handler over a Unix socket via httptest — hooked
// up by discarding httptest's default TCP listener and substituting a Unix
// one before Start, since DockerClient always dials a socket path. calls
// counts every request the fake daemon actually received, so a test can
// assert zero Docker calls happened for a refused command.
func fakeDockerServer(t *testing.T, handler http.HandlerFunc) (sockPath string, calls *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		handler(w, r)
	}))
	require.NoError(t, srv.Listener.Close())

	sockPath = filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)

	return sockPath, &n
}

func TestCommands_CatalogFromConfig(t *testing.T) {
	cfg := loadCommandsConfig(t, "schema_version: 1\ncommands:\n  allow_restart:\n    - sa-demo-web\n    - plex\n")
	src := New(cfg, unraid.NewDockerClient("/unused.sock"), noopLogger())

	cmds, err := src.Commands(context.Background())
	require.NoError(t, err)
	require.Len(t, cmds, 2)

	byID := map[string]Command{}
	for _, c := range cmds {
		byID[c.ID] = c
	}
	demo, ok := byID["restart-container:sa-demo-web"]
	require.True(t, ok, "expected a restart-container:sa-demo-web entry, got %+v", cmds)
	assert.Equal(t, "Restart sa-demo-web", demo.Label)
	assert.Contains(t, demo.Consequence, "sa-demo-web")

	_, ok = byID["restart-container:plex"]
	require.True(t, ok, "expected a restart-container:plex entry, got %+v", cmds)
}

func TestCommands_EmptyAllowlistYieldsEmptyCatalog(t *testing.T) {
	cfg := loadCommandsConfig(t, "schema_version: 1\n")
	require.Empty(t, cfg.AllowRestart, "default allow_restart must be empty")
	src := New(cfg, unraid.NewDockerClient("/unused.sock"), noopLogger())

	cmds, err := src.Commands(context.Background())
	require.NoError(t, err)
	assert.Empty(t, cmds, "an empty allowlist must yield zero runnable commands")
}

func TestRun_UnknownIDRefusedWithoutDockerCall(t *testing.T) {
	sock, calls := fakeDockerServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	cfg := loadCommandsConfig(t, "schema_version: 1\ncommands:\n  allow_restart:\n    - sa-demo-web\n  timeout: 2s\n")
	src := New(cfg, unraid.NewDockerClient(sock), noopLogger())

	_, err := src.Run(context.Background(), "not-a-real-command", "operator")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownCommand))
	assert.Equal(t, int32(0), atomic.LoadInt32(calls), "an unknown id must never reach Docker")
}

func TestRun_MalformedIDRefusedWithoutDockerCall(t *testing.T) {
	sock, calls := fakeDockerServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	cfg := loadCommandsConfig(t, "schema_version: 1\ncommands:\n  allow_restart:\n    - sa-demo-web\n  timeout: 2s\n")
	src := New(cfg, unraid.NewDockerClient(sock), noopLogger())

	_, err := src.Run(context.Background(), "restart-container:", "operator")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownCommand))
	assert.Equal(t, int32(0), atomic.LoadInt32(calls))
}

// TestRun_NotAllowlistedRefusedWithoutDockerCall is the request-forgery
// case: a well-formed id naming a real verb, but a target that was never
// (or is no longer) in the config allowlist. Never trust the id alone.
func TestRun_NotAllowlistedRefusedWithoutDockerCall(t *testing.T) {
	sock, calls := fakeDockerServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	cfg := loadCommandsConfig(t, "schema_version: 1\ncommands:\n  allow_restart:\n    - sa-demo-web\n  timeout: 2s\n")
	src := New(cfg, unraid.NewDockerClient(sock), noopLogger())

	_, err := src.Run(context.Background(), "restart-container:some-real-service", "operator")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownCommand))
	assert.Equal(t, int32(0), atomic.LoadInt32(calls), "a well-formed id for a non-allowlisted target must never reach Docker")
}

func TestRun_SuccessfulRestart(t *testing.T) {
	sock, calls := fakeDockerServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/containers/sa-demo-web/restart", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	cfg := loadCommandsConfig(t, "schema_version: 1\ncommands:\n  allow_restart:\n    - sa-demo-web\n  timeout: 2s\n")
	src := New(cfg, unraid.NewDockerClient(sock), noopLogger())

	result, err := src.Run(context.Background(), "restart-container:sa-demo-web", "operator")
	require.NoError(t, err)
	assert.True(t, result.OK)
	assert.NotEmpty(t, result.Output)
	assert.False(t, result.StartedAt.IsZero())
	assert.False(t, result.FinishedAt.IsZero())
	assert.False(t, result.FinishedAt.Before(result.StartedAt))
	assert.Equal(t, int32(1), atomic.LoadInt32(calls))
}

func TestRun_DockerFailureSurfacesAsError(t *testing.T) {
	sock, calls := fakeDockerServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	cfg := loadCommandsConfig(t, "schema_version: 1\ncommands:\n  allow_restart:\n    - sa-demo-web\n  timeout: 2s\n")
	src := New(cfg, unraid.NewDockerClient(sock), noopLogger())

	result, err := src.Run(context.Background(), "restart-container:sa-demo-web", "operator")
	require.Error(t, err, "a Docker API failure must surface as a real error, never a fabricated success")
	assert.False(t, result.OK)
	assert.NotEmpty(t, result.Output)
	assert.Equal(t, int32(1), atomic.LoadInt32(calls))
}

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

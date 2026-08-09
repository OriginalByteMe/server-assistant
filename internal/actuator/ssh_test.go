package actuator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// fakeRunner is a canned SSH command runner — no network (CONVENTIONS rule 9).
// It records the command it was asked to run so a test can assert the
// Actuator issues bounded, scoped commands.
type fakeRunner struct {
	out  string
	err  error
	last string
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (string, error) {
	f.last = cmd
	return f.out, f.err
}

func TestNewSSH(t *testing.T) {
	r := &fakeRunner{}
	a := NewSSH(r, []string{"plex", "nginx", "db"})
	require.NotNil(t, a)
	require.True(t, len(a.allow) == 3, "allowlist should contain all three containers")
}

func TestRestartContainer(t *testing.T) {
	tests := []struct {
		name      string
		allow     []string
		container string
		runnerErr error
		expectCmd string
		expectErr bool
		errMsg    string
	}{
		{
			name:      "restart allowed container",
			allow:     []string{"plex", "nginx"},
			container: "plex",
			expectCmd: "docker restart plex",
		},
		{
			name:      "another allowed container",
			allow:     []string{"plex", "nginx"},
			container: "nginx",
			expectCmd: "docker restart nginx",
		},
		{
			name:      "container not in allowlist",
			allow:     []string{"plex"},
			container: "forbidden",
			expectErr: true,
			expectCmd: "",
			errMsg:    "not in the restart allowlist",
		},
		{
			name:      "invalid container name (shell injection attempt)",
			allow:     []string{"plex", "plex && rm -rf /"},
			container: "plex && rm -rf /",
			expectErr: true,
			expectCmd: "",
			errMsg:    "invalid container name",
		},
		{
			name:      "invalid container name (semicolon)",
			allow:     []string{"plex"},
			container: "plex; exit",
			expectErr: true,
			expectCmd: "",
			errMsg:    "invalid container name",
		},
		{
			name:      "invalid container name (space)",
			allow:     []string{"plex"},
			container: "plex app",
			expectErr: true,
			expectCmd: "",
			errMsg:    "invalid container name",
		},
		{
			name:      "valid names with allowed chars",
			allow:     []string{"my-app", "db_server", "cache.service"},
			container: "my-app",
			expectCmd: "docker restart my-app",
		},
		{
			name:      "runner error propagates",
			allow:     []string{"plex"},
			container: "plex",
			runnerErr: errors.New("ssh: connection refused"),
			expectErr: true,
			expectCmd: "docker restart plex",
			errMsg:    "connection refused",
		},
		{
			name:      "empty container name",
			allow:     []string{"plex"},
			container: "",
			expectErr: true,
			expectCmd: "",
			errMsg:    "invalid container name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{err: tt.runnerErr}
			a := NewSSH(r, tt.allow)
			err := a.RestartContainer(context.Background(), tt.container)

			if tt.expectErr {
				require.Error(t, err, "expected error")
				if tt.errMsg != "" {
					require.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
			}

			if tt.expectCmd == "" {
				require.Empty(t, r.last, "expected no runner call")
			} else {
				require.Equal(t, tt.expectCmd, r.last, "runner called with wrong command")
			}
		})
	}
}

func TestHealthy(t *testing.T) {
	tests := []struct {
		name      string
		runnerOut string
		runnerErr error
		expectErr bool
	}{
		{
			name:      "healthy with version output",
			runnerOut: "20.10.12\n",
			expectErr: false,
		},
		{
			name:      "healthy with whitespace",
			runnerOut: "  20.10.12  \n",
			expectErr: false,
		},
		{
			name:      "unhealthy: empty output",
			runnerOut: "",
			expectErr: true,
		},
		{
			name:      "unhealthy: whitespace only",
			runnerOut: "   \n",
			expectErr: true,
		},
		{
			name:      "runner error propagates",
			runnerErr: errors.New("docker: daemon not running"),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{out: tt.runnerOut, err: tt.runnerErr}
			a := NewSSH(r, []string{})
			err := a.Healthy(context.Background())

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			// Healthy must always call the runner with docker version command
			require.Equal(t, "docker version --format {{.Server.Version}}", r.last)
		})
	}
}

var _ core.Actuator = (*SSH)(nil)

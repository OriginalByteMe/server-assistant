package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// fakeRunner is a canned SSH command runner — no network (CONVENTIONS rule 9).
// It records the command it was asked to run so a test can assert the tool
// issues a bounded, read-only command.
type fakeRunner struct {
	out  string
	err  error
	last string
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (string, error) {
	f.last = cmd
	return f.out, f.err
}

// fakeStore implements the minimal Store interface needed for status_history
// tests. Only LoadProbeSamples does real work; other methods return zero
// values.
type fakeStore struct {
	samples []core.ProbeSample
}

func (f *fakeStore) Migrate(_ context.Context) error { return nil }

func (f *fakeStore) RecordProbe(_ context.Context, _ core.ProbeSample) error { return nil }

func (f *fakeStore) LoadProbeSamples(_ context.Context, service string, limit int) ([]core.ProbeSample, error) {
	// Return whatever samples were seeded for this test.
	return f.samples, nil
}

func (f *fakeStore) PruneProbeSamples(_ context.Context, _ string, _ time.Time) error { return nil }

func (f *fakeStore) SaveCommittedStatus(_ context.Context, _ core.CommittedStatus) error { return nil }

func (f *fakeStore) LoadCommittedStatuses(_ context.Context) ([]core.CommittedStatus, error) {
	return nil, nil
}

func (f *fakeStore) SaveHarnessCycle(_ context.Context, _ core.HarnessCycle) error { return nil }

func (f *fakeStore) ListHarnessCycles(_ context.Context, _ int) ([]core.HarnessCycle, error) {
	return nil, nil
}

func (f *fakeStore) GetHarnessCycle(_ context.Context, _ string) (core.HarnessCycle, error) {
	return core.HarnessCycle{}, nil
}

func (f *fakeStore) Close() error { return nil }

func TestContainerStatus(t *testing.T) {
	tests := []struct {
		name      string
		targets   map[string]string
		args      map[string]string
		runnerOut string
		runnerErr error
		expectCmd string
		expectOut string
		expectErr bool
	}{
		{
			name:      "known service, running and healthy",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "plex"},
			runnerOut: "running|healthy\n",
			expectCmd: `docker inspect -f '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' plex`,
			expectOut: "state=running health=healthy",
		},
		{
			name:      "known service, exited",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "plex"},
			runnerOut: "exited|\n",
			expectCmd: `docker inspect -f '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' plex`,
			expectOut: "state=exited health=",
		},
		{
			name:      "known service with underscores and dots",
			targets:   map[string]string{"nginx-app": "nginx_app.prod"},
			args:      map[string]string{"service": "nginx-app"},
			expectCmd: `docker inspect -f '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' nginx_app.prod`,
			runnerOut: "running|healthy\n",
			expectOut: "state=running health=healthy",
		},
		{
			name:      "unknown service",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "unknown"},
			expectErr: true,
			// Verify no runner call was made:
			expectCmd: "",
		},
		{
			name:      "missing service arg",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{},
			expectErr: true,
			expectCmd: "",
		},
		{
			name:      "service resolves to invalid container name",
			targets:   map[string]string{"bad": "plex; rm -rf /"},
			args:      map[string]string{"service": "bad"},
			expectErr: true,
			expectCmd: "",
		},
		{
			name:      "runner error propagates",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "plex"},
			runnerErr: errors.New("ssh: connection refused"),
			expectErr: true,
			expectCmd: `docker inspect -f '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' plex`,
		},
		{
			name:      "malformed output (missing separator)",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "plex"},
			runnerOut: "running\n",
			expectErr: true,
			expectCmd: `docker inspect -f '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' plex`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{out: tt.runnerOut, err: tt.runnerErr}
			tool := ContainerStatus(r, tt.targets)
			out, err := tool.Call(context.Background(), tt.args)

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectOut, out)
			}

			if tt.expectCmd == "" {
				require.Empty(t, r.last, "expected no runner call")
			} else {
				require.Equal(t, tt.expectCmd, r.last, "runner called with wrong command")
			}
		})
	}
}

func TestContainerLogs(t *testing.T) {
	tests := []struct {
		name      string
		targets   map[string]string
		args      map[string]string
		lines     int
		runnerOut string
		runnerErr error
		expectCmd string
		expectOut string
		expectErr bool
	}{
		{
			name:      "default lines (50)",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "plex"},
			lines:     0, // triggers default
			runnerOut: "log line 1\nlog line 2\n",
			expectCmd: "docker logs --tail 50 plex 2>&1",
			expectOut: "log line 1\nlog line 2\n",
		},
		{
			name:      "explicit lines clamped to max (200)",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "plex"},
			lines:     500,
			runnerOut: "logs",
			expectCmd: "docker logs --tail 200 plex 2>&1",
			expectOut: "logs",
		},
		{
			name:      "negative lines uses default",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "plex"},
			lines:     -5,
			runnerOut: "logs",
			expectCmd: "docker logs --tail 50 plex 2>&1",
			expectOut: "logs",
		},
		{
			name:      "lines within range",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "plex"},
			lines:     75,
			runnerOut: "logs",
			expectCmd: "docker logs --tail 75 plex 2>&1",
			expectOut: "logs",
		},
		{
			name:      "unknown service",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "unknown"},
			lines:     50,
			expectErr: true,
			expectCmd: "",
		},
		{
			name:      "service resolves to invalid container name",
			targets:   map[string]string{"bad": "plex && exit"},
			args:      map[string]string{"service": "bad"},
			lines:     50,
			expectErr: true,
			expectCmd: "",
		},
		{
			name:      "runner error propagates",
			targets:   map[string]string{"plex": "plex"},
			args:      map[string]string{"service": "plex"},
			lines:     50,
			runnerErr: errors.New("docker: not running"),
			expectErr: true,
			expectCmd: "docker logs --tail 50 plex 2>&1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{out: tt.runnerOut, err: tt.runnerErr}
			tool := ContainerLogs(r, tt.targets, tt.lines)
			out, err := tool.Call(context.Background(), tt.args)

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectOut, out)
			}

			if tt.expectCmd == "" {
				require.Empty(t, r.last, "expected no runner call")
			} else {
				require.Equal(t, tt.expectCmd, r.last, "runner called with wrong command")
			}
		})
	}
}

func TestStatusHistory(t *testing.T) {
	t1 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 1, 12, 5, 0, 0, time.UTC)
	t3 := time.Date(2024, 1, 1, 12, 10, 0, 0, time.UTC)

	tests := []struct {
		name      string
		args      map[string]string
		limit     int
		samples   []core.ProbeSample
		expectErr bool
		expectOut string
	}{
		{
			name:  "renders recent samples",
			args:  map[string]string{"service": "plex"},
			limit: 20,
			samples: []core.ProbeSample{
				{Service: "plex", Status: core.StatusUp, Latency: 100 * time.Millisecond, At: t1},
				{Service: "plex", Status: core.StatusDegraded, Latency: 500 * time.Millisecond, At: t2},
				{Service: "plex", Status: core.StatusDown, Latency: 50 * time.Millisecond, At: t3},
			},
			expectOut: "2024-01-01T12:00:00Z UP 100ms\n2024-01-01T12:05:00Z DEGRADED 500ms\n2024-01-01T12:10:00Z DOWN 50ms",
		},
		{
			name:      "missing service arg",
			args:      map[string]string{},
			limit:     20,
			expectErr: true,
		},
		{
			name:      "empty sample list",
			args:      map[string]string{"service": "nonexistent"},
			limit:     20,
			samples:   []core.ProbeSample{},
			expectOut: "",
		},
		{
			name:  "default limit (20)",
			args:  map[string]string{"service": "plex"},
			limit: 0,
			samples: []core.ProbeSample{
				{Service: "plex", Status: core.StatusUp, Latency: 100 * time.Millisecond, At: t1},
			},
			expectOut: "2024-01-01T12:00:00Z UP 100ms",
		},
		{
			name:  "limit clamped to max (100)",
			args:  map[string]string{"service": "plex"},
			limit: 500,
			samples: []core.ProbeSample{
				{Service: "plex", Status: core.StatusUp, Latency: 10 * time.Nanosecond, At: t1},
			},
			expectOut: "2024-01-01T12:00:00Z UP 10ns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{samples: tt.samples}
			tool := StatusHistory(store, tt.limit)
			out, err := tool.Call(context.Background(), tt.args)

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectOut, out)
			}
		})
	}
}

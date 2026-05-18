package prober

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// fakeRunner is a canned SSH command runner — no network (CONVENTIONS rule 9).
// It records the command it was asked to run so a test can assert the probe
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

// A container that is running and healthy ⇒ the Service is UP.
func TestContainerProbe_RunningHealthyIsUp(t *testing.T) {
	r := &fakeRunner{out: "running|healthy\n"}
	p := NewContainerProbe("plex", r, "plex")
	res, err := p.Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, core.StatusUp, res.Status)
	require.Contains(t, r.last, "plex", "probe targets the configured container")
}

// Running but the container's healthcheck is failing ⇒ DEGRADED (reachable
// but not doing its job well) — not DOWN, not UP (CONTEXT.md).
func TestContainerProbe_RunningUnhealthyIsDegraded(t *testing.T) {
	r := &fakeRunner{out: "running|unhealthy\n"}
	p := NewContainerProbe("plex", r, "plex")
	res, err := p.Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, core.StatusDegraded, res.Status)
}

// A container that exists but is not running (exited / restarting / created /
// dead) ⇒ DOWN: confirmed not doing its job.
func TestContainerProbe_NotRunningIsDown(t *testing.T) {
	for _, state := range []string{"exited|", "restarting|", "created|", "dead|"} {
		r := &fakeRunner{out: state + "\n"}
		p := NewContainerProbe("plex", r, "plex")
		res, err := p.Probe(context.Background())
		require.NoError(t, err)
		require.Equal(t, core.StatusDown, res.Status, "state %q must be DOWN", state)
	}
}

// An SSH/command failure is NOT a measurement of the container — surfacing
// DOWN would let an SSH blip commit a false outage. The observer never
// collapses "can't tell" into "down" (rule 5 / ADR 0005): return an error so
// the monitor skips this probe (it never commits DOWN from it).
func TestContainerProbe_RunnerErrorIsNotDown(t *testing.T) {
	r := &fakeRunner{err: errors.New("ssh: connection refused")}
	p := NewContainerProbe("plex", r, "plex")
	res, err := p.Probe(context.Background())
	require.Error(t, err)
	require.NotEqual(t, core.StatusDown, res.Status, "SSH failure must never yield DOWN (rule 5/ADR 0005)")
}

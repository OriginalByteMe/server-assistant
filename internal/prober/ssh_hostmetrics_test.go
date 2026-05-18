package prober

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// The Host-metrics probe reads one structured key=value report over SSH and
// derives Host Status from array/disk/parity + CPU/RAM. A healthy Unraid box
// (array STARTED, no disabled/invalid disks, sane load and free memory) ⇒ UP.
func TestHostMetricsProbe_HealthyIsUp(t *testing.T) {
	r := &fakeRunner{out: "mdState=STARTED\nmdNumDisabled=0\nmdNumInvalid=0\nload1=0.50\ncpus=8\nmemTotal=16000000\nmemAvailable=8000000\n"}
	p := NewHostMetricsProbe("unraid", r)
	res, err := p.Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, core.StatusUp, res.Status)
}

// Array not STARTED ⇒ the Host is not doing its job ⇒ DOWN.
func TestHostMetricsProbe_ArrayNotStartedIsDown(t *testing.T) {
	r := &fakeRunner{out: "mdState=STOPPED\nmdNumDisabled=0\nmdNumInvalid=0\nload1=0.1\ncpus=8\nmemTotal=16000000\nmemAvailable=9000000\n"}
	p := NewHostMetricsProbe("unraid", r)
	res, err := p.Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, core.StatusDown, res.Status)
}

// A disabled/invalid disk (failed drive / invalid parity) ⇒ DEGRADED: the
// array is up but redundancy/health is compromised — not a clean UP, not a
// full DOWN.
func TestHostMetricsProbe_DiskOrParityProblemIsDegraded(t *testing.T) {
	r := &fakeRunner{out: "mdState=STARTED\nmdNumDisabled=1\nmdNumInvalid=0\nload1=0.2\ncpus=8\nmemTotal=16000000\nmemAvailable=9000000\n"}
	p := NewHostMetricsProbe("unraid", r)
	res, err := p.Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, core.StatusDegraded, res.Status)
}

// Sustained CPU overload or memory pressure ⇒ DEGRADED (reachable but slow).
func TestHostMetricsProbe_ResourcePressureIsDegraded(t *testing.T) {
	overload := &fakeRunner{out: "mdState=STARTED\nmdNumDisabled=0\nmdNumInvalid=0\nload1=32\ncpus=8\nmemTotal=16000000\nmemAvailable=9000000\n"}
	res, err := NewHostMetricsProbe("unraid", overload).Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, core.StatusDegraded, res.Status)

	lowmem := &fakeRunner{out: "mdState=STARTED\nmdNumDisabled=0\nmdNumInvalid=0\nload1=0.1\ncpus=8\nmemTotal=16000000\nmemAvailable=200000\n"}
	res, err = NewHostMetricsProbe("unraid", lowmem).Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, core.StatusDegraded, res.Status)
}

// An SSH failure or a report missing the critical array field is "can't
// tell", never DOWN (rule 5 / ADR 0005): surface an error so the monitor
// skips it (and ARK-12's gate, not this probe, owns the UNKNOWN).
func TestHostMetricsProbe_RunnerErrorOrUnparseableIsNotDown(t *testing.T) {
	rErr := &fakeRunner{err: errors.New("ssh: handshake failed")}
	res, err := NewHostMetricsProbe("unraid", rErr).Probe(context.Background())
	require.Error(t, err)
	require.NotEqual(t, core.StatusDown, res.Status)

	rJunk := &fakeRunner{out: "garbage without mdState\n"}
	res, err = NewHostMetricsProbe("unraid", rJunk).Probe(context.Background())
	require.Error(t, err)
	require.NotEqual(t, core.StatusDown, res.Status)
}

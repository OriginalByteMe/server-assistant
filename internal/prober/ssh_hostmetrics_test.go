package prober

import (
	"context"
	"errors"
	"fmt"
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

// Codex P2 (rule 5 / ADR 0005): the disk/parity counters are part of the
// derivation. If the report has mdState=STARTED but omits or corrupts
// mdNumDisabled / mdNumInvalid (truncated output, changed mdcmd format),
// defaulting them to 0 would report a false clean UP. Unknown disk health is
// "can't tell" — surface an error so the monitor skips it, never UP.
func TestHostMetricsProbe_MissingOrInvalidDiskCountersIsNotUp(t *testing.T) {
	cases := []string{
		"mdState=STARTED\nmdNumInvalid=0\nload1=0.1\ncpus=8\nmemTotal=16000000\nmemAvailable=9000000\n",                     // mdNumDisabled missing
		"mdState=STARTED\nmdNumDisabled=0\nload1=0.1\ncpus=8\nmemTotal=16000000\nmemAvailable=9000000\n",                    // mdNumInvalid missing
		"mdState=STARTED\nmdNumDisabled=oops\nmdNumInvalid=0\nload1=0.1\ncpus=8\nmemTotal=16000000\nmemAvailable=9000000\n", // mdNumDisabled corrupt
	}
	for _, out := range cases {
		res, err := NewHostMetricsProbe("unraid", &fakeRunner{out: out}).Probe(context.Background())
		require.Error(t, err, "report %q must error", out)
		require.NotEqual(t, core.StatusUp, res.Status, "unknown disk health must never derive UP (rule 5)")
	}
}

// Resource metrics (load/cpu/mem) are derivation inputs too: a report truncated
// after the array block, or a non-numeric value, must surface an error
// ("can't tell"), never a false-clean UP (rule 5 / ADR 0005). Mirrors the disk
// counter guard above.
func TestHostMetricsProbe_MissingOrInvalidResourceMetricIsNotUp(t *testing.T) {
	cases := []string{
		"mdState=STARTED\nmdNumDisabled=0\nmdNumInvalid=0\ncpus=8\nmemTotal=16000000\nmemAvailable=9000000\n",             // load1 missing (truncated)
		"mdState=STARTED\nmdNumDisabled=0\nmdNumInvalid=0\nload1=0.1\nmemTotal=16000000\nmemAvailable=9000000\n",          // cpus missing
		"mdState=STARTED\nmdNumDisabled=0\nmdNumInvalid=0\nload1=0.1\ncpus=8\nmemAvailable=9000000\n",                     // memTotal missing
		"mdState=STARTED\nmdNumDisabled=0\nmdNumInvalid=0\nload1=0.1\ncpus=8\nmemTotal=16000000\n",                        // memAvailable missing
		"mdState=STARTED\nmdNumDisabled=0\nmdNumInvalid=0\nload1=oops\ncpus=8\nmemTotal=16000000\nmemAvailable=9000000\n", // load1 non-numeric
	}
	for _, out := range cases {
		res, err := NewHostMetricsProbe("unraid", &fakeRunner{out: out}).Probe(context.Background())
		require.Error(t, err, "report %q must error", out)
		require.NotEqual(t, core.StatusUp, res.Status, "unknown resource health must never derive UP (rule 5)")
	}
}

func TestHostMetricsProbe_TransportUnreachableIsDown(t *testing.T) {
	r := &fakeRunner{err: fmt.Errorf("ssh dial: %w: %w", ErrUnreachable, errors.New("connection refused"))}
	res, err := NewHostMetricsProbe("unraid", r).Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, core.StatusDown, res.Status)
}

func TestHostMetricsProbe_NonTransportErrorSkips(t *testing.T) {
	r := &fakeRunner{err: errors.New("boom")}
	res, err := NewHostMetricsProbe("unraid", r).Probe(context.Background())
	require.Error(t, err)
	require.NotEqual(t, core.StatusUp, res.Status)
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

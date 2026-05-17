package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A Status change commits only after N consecutive agreeing Probes; before the
// Nth, the committed Status is unchanged and no change is reported (CONTEXT.md
// debounce). The Debouncer starts blind: committed Status is UNKNOWN.
func TestDebouncer_CommitsAfterNAgreeing(t *testing.T) {
	d := NewDebouncer(3)

	got, changed := d.Observe(StatusUp)
	require.Equal(t, StatusUnknown, got)
	require.False(t, changed)

	got, changed = d.Observe(StatusUp)
	require.Equal(t, StatusUnknown, got)
	require.False(t, changed)

	got, changed = d.Observe(StatusUp)
	require.Equal(t, StatusUp, got)
	require.True(t, changed)
}

// A flapping Service that never reaches N consecutive agreeing Probes never
// commits — the committed Status stays UNKNOWN and no change is ever reported
// (AC #3: flapping does not commit).
func TestDebouncer_FlappingNeverCommits(t *testing.T) {
	d := NewDebouncer(3)
	for _, s := range []Status{StatusUp, StatusDown, StatusUp, StatusDown, StatusUp, StatusDown} {
		got, changed := d.Observe(s)
		require.Equal(t, StatusUnknown, got)
		require.False(t, changed)
	}
}

// After a committed DOWN, N consecutive UP Probes commit the recovery and
// report a change — an Alert fires on recovery to UP (CONTEXT.md). A single
// UP blip mid-DOWN-streak does not derail the original commit.
func TestDebouncer_RecoveryToUpCommits(t *testing.T) {
	d := NewDebouncer(2)

	require.Equal(t, StatusUnknown, mustObserve(t, d, StatusDown, false))
	committed, changed := d.Observe(StatusDown)
	require.Equal(t, StatusDown, committed)
	require.True(t, changed) // DOWN committed

	// One UP blip then back to DOWN: not 2 consecutive UP, no recovery commit.
	require.Equal(t, StatusDown, mustObserve(t, d, StatusUp, false))
	require.Equal(t, StatusDown, mustObserve(t, d, StatusDown, false))

	// Now 2 consecutive UP: recovery commits.
	require.Equal(t, StatusDown, mustObserve(t, d, StatusUp, false))
	committed, changed = d.Observe(StatusUp)
	require.Equal(t, StatusUp, committed)
	require.True(t, changed) // recovery to UP committed
}

// A Debouncer seeded with a previously committed Status (restart resume —
// CONTEXT.md) does not re-fire for that same Status, but still commits a
// genuine change after N agreeing Probes. This is what keeps a restart from
// re-alerting an already-known DOWN.
func TestDebouncer_SeededCommittedDoesNotReAlert(t *testing.T) {
	d := NewDebouncerWithStatus(2, StatusDown)

	// Already DOWN: re-observing DOWN is not a change.
	require.Equal(t, StatusDown, mustObserve(t, d, StatusDown, false))
	require.Equal(t, StatusDown, mustObserve(t, d, StatusDown, false))

	// A real recovery still commits after N agreeing UP.
	require.Equal(t, StatusDown, mustObserve(t, d, StatusUp, false))
	committed, changed := d.Observe(StatusUp)
	require.Equal(t, StatusUp, committed)
	require.True(t, changed)
}

func mustObserve(t *testing.T, d *Debouncer, s Status, wantChanged bool) Status {
	t.Helper()
	got, changed := d.Observe(s)
	require.Equal(t, wantChanged, changed)
	return got
}

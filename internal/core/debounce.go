package core

// Debouncer absorbs flapping: a candidate Status must be observed on N
// consecutive Probes before it commits. Only a committed change fires an
// Alert (CONTEXT.md). It is a pure state machine — no time, no I/O — so the
// monitor loop owns scheduling and this stays trivially testable.
//
// The zero observer is blind: the committed Status starts UNKNOWN until N
// agreeing Probes prove otherwise (ADR 0005).
type Debouncer struct {
	n         int
	committed Status
	candidate Status
	streak    int
}

// NewDebouncer returns a Debouncer requiring n consecutive agreeing Probes to
// commit a change. n < 1 is treated as 1 (every Probe commits immediately).
// The committed Status starts UNKNOWN — the observer is blind until proven.
func NewDebouncer(n int) *Debouncer {
	return NewDebouncerWithStatus(n, StatusUnknown)
}

// NewDebouncerWithStatus seeds the committed Status from a prior run so a
// restart resumes instead of re-alerting an already-known Status (CONTEXT.md
// restart-safety). Same N-consecutive rule governs subsequent changes.
func NewDebouncerWithStatus(n int, committed Status) *Debouncer {
	if n < 1 {
		n = 1
	}
	return &Debouncer{n: n, committed: committed}
}

// Observe records one derived Status. It returns the currently committed
// Status and whether this Probe caused a commit (a committed change).
func (d *Debouncer) Observe(s Status) (committed Status, changed bool) {
	if s == d.candidate {
		d.streak++
	} else {
		d.candidate = s
		d.streak = 1
	}

	if d.candidate != d.committed && d.streak >= d.n {
		d.committed = d.candidate
		return d.committed, true
	}
	return d.committed, false
}

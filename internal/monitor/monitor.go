// Package monitor is the v1 monitoring spine: it drives each configured
// Service through Probe → DeriveStatus → debounce → persist → Alert, and
// publishes committed views for the dashboard. It owns scheduling; the pure
// core logic (DeriveStatus, Debouncer) stays I/O-free and unit-tested.
package monitor

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"server-assistant/internal/core"
)

// Service is one monitored unit handed to the Monitor: a named Prober plus
// the per-Service knobs honored from config (CONTEXT.md / rule 6).
type Service struct {
	Name      string
	Prober    core.Prober
	Threshold time.Duration
	Poll      time.Duration
	DebounceN int
}

type serviceRuntime struct {
	name      string
	prober    core.Prober
	threshold time.Duration
	poll      time.Duration
	debounceN int
	deb       *core.Debouncer
}

// Monitor runs the poll loops and serves the dashboard's current view.
type Monitor struct {
	store    core.Store
	notifier core.Notifier
	svcs     []*serviceRuntime

	mu    sync.RWMutex
	views map[string]core.ServiceView

	hub *hub
}

// New builds a Monitor. Call Resume before Run to restore committed Status
// from the Store so a restart does not re-alert (CONTEXT.md restart-safety).
func New(store core.Store, notifier core.Notifier, svcs []Service) *Monitor {
	m := &Monitor{
		store:    store,
		notifier: notifier,
		views:    make(map[string]core.ServiceView, len(svcs)),
		hub:      newHub(),
	}
	for _, s := range svcs {
		m.svcs = append(m.svcs, &serviceRuntime{
			name:      s.Name,
			prober:    s.Prober,
			threshold: s.Threshold,
			poll:      s.Poll,
			debounceN: s.DebounceN,
			deb:       core.NewDebouncer(s.DebounceN),
		})
		m.views[s.Name] = core.ServiceView{Name: s.Name, Status: core.StatusUnknown}
	}
	return m
}

// Resume seeds each Service's debounce and view from the last committed
// Status so the daemon picks up where it left off instead of re-alerting.
func (m *Monitor) Resume(ctx context.Context) error {
	saved, err := m.store.LoadCommittedStatuses(ctx)
	if err != nil {
		return err
	}
	byName := make(map[string]core.CommittedStatus, len(saved))
	for _, cs := range saved {
		byName[cs.Service] = cs
	}
	for _, s := range m.svcs {
		cs, ok := byName[s.name]
		if !ok {
			continue
		}
		s.deb = core.NewDebouncerWithStatus(s.debounceN, cs.Status)
		m.mu.Lock()
		m.views[s.name] = core.ServiceView{Name: s.name, Status: cs.Status, LastChecked: cs.ChangedAt}
		m.mu.Unlock()
	}
	return nil
}

// Run starts a poll loop per Service and blocks until ctx is cancelled, then
// waits for every loop to exit cleanly (CONVENTIONS rule 4).
func (m *Monitor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range m.svcs {
		wg.Add(1)
		go func(s *serviceRuntime) {
			defer wg.Done()
			t := time.NewTicker(s.poll)
			defer t.Stop()
			m.probeOnce(ctx, s) // probe immediately, don't wait a full interval
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					m.probeOnce(ctx, s)
				}
			}
		}(s)
	}
	<-ctx.Done()
	wg.Wait()
}

func (m *Monitor) probeOnce(ctx context.Context, s *serviceRuntime) {
	res, err := s.prober.Probe(ctx)
	if err != nil {
		slog.Error("probe error", "service", s.name, "err", err)
		return
	}
	derived := core.DeriveStatus(res, s.threshold)
	now := time.Now().UTC()

	if rerr := m.store.RecordProbe(ctx, core.ProbeSample{
		Service: s.name, Status: derived, Latency: res.Latency, At: now,
	}); rerr != nil {
		slog.Error("record probe", "service", s.name, "err", rerr)
	}

	committed, changed := s.deb.Observe(derived)
	view := core.ServiceView{Name: s.name, Status: committed, Latency: res.Latency, LastChecked: now}
	m.setView(view)

	if changed {
		if serr := m.store.SaveCommittedStatus(ctx, core.CommittedStatus{
			Service: s.name, Status: committed, ChangedAt: now,
		}); serr != nil {
			slog.Error("save committed status", "service", s.name, "err", serr)
		}
		if nerr := m.notifier.Notify(ctx, core.Alert{
			Subject: s.name,
			Status:  committed,
			Message: s.name + " is now " + committed.String(),
		}); nerr != nil {
			slog.Error("notify", "service", s.name, "err", nerr)
		}
		slog.Info("committed status change", "service", s.name, "status", committed.String())
	}
	// Push every probe so the dashboard's latency / last-checked stay live;
	// Alerts (above) remain strictly debounced.
	m.hub.broadcast(view)
}

func (m *Monitor) setView(v core.ServiceView) {
	m.mu.Lock()
	m.views[v.Name] = v
	m.mu.Unlock()
}

// Snapshot returns the current view of every Service, sorted by name, for the
// dashboard's initial server-rendered page.
func (m *Monitor) Snapshot() []core.ServiceView {
	m.mu.RLock()
	out := make([]core.ServiceView, 0, len(m.views))
	for _, v := range m.views {
		out = append(out, v)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Subscribe registers an SSE listener. The returned cancel func must be
// called to release it.
func (m *Monitor) Subscribe() (<-chan core.ServiceView, func()) {
	return m.hub.subscribe()
}

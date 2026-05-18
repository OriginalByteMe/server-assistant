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
	"sync/atomic"
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

// Host is the single monitored Unraid box as a first-class subject (CONTEXT.md;
// ADR 0005). Its reachability Probe gates every Service: when the Server
// Assistant box cannot reach the Host, its Services become UNKNOWN (never
// DOWN) and exactly one "Host unreachable" Alert fires. Optional — a Monitor
// with no Host set has no gate and behaves exactly as the bare spine
// (backward-compatible, ADR 0006 rule 2).
type Host struct {
	Name      string
	Prober    core.Prober
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

type hostRuntime struct {
	name      string
	prober    core.Prober
	poll      time.Duration
	debounceN int
	deb       *core.Debouncer
}

// Monitor runs the poll loops and serves the dashboard's current view.
type Monitor struct {
	store    core.Store
	notifier core.Notifier
	svcs     []*serviceRuntime

	host *hostRuntime
	// gate is the ADR 0005 reachability gate: true = Host reachable (or no
	// Host configured) so Services derive Status normally; false = blind, so
	// Services are UNKNOWN and their Probers are not called. It tracks the
	// LATEST Host Probe (not the debounced commit) so a false DOWN can never
	// slip through the debounce window (CONVENTIONS rule 5).
	gate atomic.Bool

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
	// No Host configured yet: the gate is open so the bare spine is unchanged.
	m.gate.Store(true)
	return m
}

// SetHost installs the Host reachability gate. Call before Resume/Run. With no
// Host set the Monitor is the bare v1 spine (ADR 0006 rule 2); with one set,
// an unreachable Host turns its Services UNKNOWN and fires exactly one "Host
// unreachable" Alert (ADR 0005). The Host is also a first-class dashboard row.
func (m *Monitor) SetHost(h Host) {
	m.host = &hostRuntime{
		name:      h.Name,
		prober:    h.Prober,
		poll:      h.Poll,
		debounceN: h.DebounceN,
		deb:       core.NewDebouncer(h.DebounceN),
	}
	m.mu.Lock()
	m.views[h.Name] = core.ServiceView{Name: h.Name, Status: core.StatusUnknown}
	m.mu.Unlock()
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
	if m.host != nil {
		if cs, ok := byName[m.host.name]; ok {
			m.host.deb = core.NewDebouncerWithStatus(m.host.debounceN, cs.Status)
			// Restore the gate from the persisted Host Status so a restart
			// resumes blind/sighted instead of re-alerting. Only a committed
			// DOWN closes the gate; UNKNOWN (never proven) leaves it open so
			// Services are not silently UNKNOWN on every boot.
			m.gate.Store(cs.Status != core.StatusDown)
			m.mu.Lock()
			m.views[m.host.name] = core.ServiceView{Name: m.host.name, Status: cs.Status, LastChecked: cs.ChangedAt}
			m.mu.Unlock()
		}
	}
	return nil
}

// Run starts a poll loop per Service and blocks until ctx is cancelled, then
// waits for every loop to exit cleanly (CONVENTIONS rule 4).
func (m *Monitor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	if m.host != nil {
		wg.Add(1)
		go func(h *hostRuntime) {
			defer wg.Done()
			t := time.NewTicker(h.poll)
			defer t.Stop()
			m.hostProbeOnce(ctx) // establish reachability before trusting Services
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					m.hostProbeOnce(ctx)
				}
			}
		}(m.host)
	}
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

// hostProbeOnce takes one Host reachability measurement. The gate is set from
// THIS Probe immediately (not the debounced commit) so a false Service DOWN
// can never slip through the debounce window (CONVENTIONS rule 5). The
// debouncer governs only the Alert: exactly one "Host unreachable" on a
// committed transition to blind, exactly one "Host reachable" on recovery —
// never one per Service, never a per-poll storm.
func (m *Monitor) hostProbeOnce(ctx context.Context) {
	h := m.host
	res, err := h.prober.Probe(ctx)
	if err != nil {
		// A canceled probe (shutdown) is not a measurement of the Host —
		// never gate or alert on it (ADR 0005, mirrors the Service path).
		slog.Error("host probe error", "host", h.name, "err", err)
		return
	}
	reachable := res.Status == core.StatusUp
	hostStatus := core.StatusDown
	if reachable {
		hostStatus = core.StatusUp
	}
	now := time.Now().UTC()

	// Publish the latest reachability to the gate and detect the transition in
	// one race-free step: Swap returns the prior value.
	wasReachable := m.gate.Swap(reachable)

	m.setView(core.ServiceView{Name: h.name, Status: hostStatus, Latency: res.Latency, LastChecked: now})

	if wasReachable && !reachable {
		// Gate just closed: every Service is now blind. Force them UNKNOWN and
		// push it so the dashboard reflects "can't tell", not a stale Status.
		for _, s := range m.svcs {
			v := core.ServiceView{Name: s.name, Status: core.StatusUnknown, LastChecked: now}
			m.setView(v)
			m.hub.broadcast(v)
		}
	}

	committed, changed := h.deb.Observe(hostStatus)
	if changed {
		if serr := m.store.SaveCommittedStatus(ctx, core.CommittedStatus{
			Service: h.name, Status: committed, ChangedAt: now,
		}); serr != nil {
			slog.Error("save committed status", "host", h.name, "err", serr)
		}
		msg := h.name + " is reachable"
		if committed == core.StatusDown {
			msg = h.name + " is unreachable"
		}
		if nerr := m.notifier.Notify(ctx, core.Alert{
			Subject: h.name, Status: committed, Message: msg,
		}); nerr != nil {
			slog.Error("notify", "host", h.name, "err", nerr)
		}
		slog.Info("committed host status change", "host", h.name, "status", committed.String())
	}
	m.hub.broadcast(core.ServiceView{Name: h.name, Status: hostStatus, Latency: res.Latency, LastChecked: now})
}

func (m *Monitor) probeOnce(ctx context.Context, s *serviceRuntime) {
	// ADR 0005 gate: while the Host is unreachable the observer is blind. Do
	// not probe at all — the debouncer stays frozen so no false DOWN can
	// commit, and the Service shows UNKNOWN (never DOWN). On recovery the
	// frozen committed Status resumes, so a still-healthy Service goes
	// UNKNOWN→UP with no Alert (no double-alert), while one that genuinely
	// died debounces to a single real DOWN after the blind window.
	if m.host != nil && !m.gate.Load() {
		m.mu.RLock()
		cur := m.views[s.name].Status
		m.mu.RUnlock()
		if cur != core.StatusUnknown {
			v := core.ServiceView{Name: s.name, Status: core.StatusUnknown, LastChecked: time.Now().UTC()}
			m.setView(v)
			m.hub.broadcast(v)
		}
		return
	}

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

package monitor

import (
	"sync"

	"server-assistant/internal/core"
)

// hub fans committed views out to SSE subscribers. A slow subscriber is
// skipped rather than allowed to block the monitor — the dashboard is a
// read-only convenience, never back-pressure on the spine.
type hub struct {
	mu   sync.Mutex
	subs map[chan core.ServiceView]struct{}
}

func newHub() *hub {
	return &hub{subs: make(map[chan core.ServiceView]struct{})}
}

func (h *hub) subscribe() (<-chan core.ServiceView, func()) {
	ch := make(chan core.ServiceView, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			close(ch)
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

func (h *hub) broadcast(v core.ServiceView) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- v:
		default: // drop for a slow consumer; never block the spine
		}
	}
}

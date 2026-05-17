package monitor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
	"server-assistant/internal/notifier"
	"server-assistant/internal/store"
)

// scriptedProber replays a fixed Status sequence, one per Probe, holding the
// last value once exhausted — a deterministic stand-in for a flapping Service
// (no timing, no network — CONVENTIONS rule 9).
type scriptedProber struct {
	seq []core.Status
	i   int
}

func (s *scriptedProber) Name() string { return "web" }
func (s *scriptedProber) Probe(context.Context) (core.ProbeResult, error) {
	st := s.seq[len(s.seq)-1]
	if s.i < len(s.seq) {
		st = s.seq[s.i]
		s.i++
	}
	return core.ProbeResult{Status: st, Latency: time.Millisecond}, nil
}

// End-to-end ARK-7 contract: drive the real monitor → debounce → real
// Telegram Notifier (pointed at a fake Bot API) through a flapping sequence
// and prove the Operator gets exactly one DOWN message and exactly one
// recovery-to-UP message — never per-poll spam. The debounce (ARK-6) absorbs
// the flaps; the Notifier only ever sees committed changes.
func TestMonitor_TelegramAlertsOnlyOnCommittedChange(t *testing.T) {
	var mu sync.Mutex
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			_ = r.ParseMultipartForm(1 << 20)
			mu.Lock()
			texts = append(texts, r.FormValue("text"))
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`)
	}))
	defer srv.Close()

	tg, err := notifier.NewTelegram("tok", "42", time.Second, bot.WithServerURL(srv.URL))
	require.NoError(t, err)

	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "i.db"))
	require.NoError(t, err)
	require.NoError(t, st.Migrate(ctx))
	defer func() { require.NoError(t, st.Close()) }()

	// Seed committed UP so the test exercises a DOWN then a recovery, not the
	// daemon's first-status commit (that path is covered elsewhere).
	require.NoError(t, st.SaveCommittedStatus(ctx, core.CommittedStatus{
		Service: "web", Status: core.StatusUp, ChangedAt: time.Now().UTC(),
	}))

	D, U := core.StatusDown, core.StatusUp
	sp := &scriptedProber{seq: []core.Status{D, U, D, D, U, D, U, U, U, D}}
	m := New(st, tg, []Service{{
		Name: "web", Prober: sp, Threshold: time.Second, Poll: time.Hour, DebounceN: 2,
	}})
	require.NoError(t, m.Resume(ctx))

	for range sp.seq {
		m.probeOnce(ctx, m.svcs[0])
	}

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, texts, 2, "exactly one DOWN + one recovery, flaps absorbed; got %v", texts)
	require.Equal(t, "web is now DOWN", texts[0])
	require.Equal(t, "web is now UP", texts[1])
}

package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

const testToken = "111222:FAKE-TOKEN"

// fakeTelegram is a stand-in Bot API server: it records every sendMessage
// request so a unit test never touches the network (CONVENTIONS rule 9).
type fakeTelegram struct {
	srv    *httptest.Server
	calls  atomic.Int64
	lastPa string
	lastCh any
	lastTx string
	status int    // HTTP status to return (default 200)
	okBody string // JSON body to return; empty => a valid ok:true message
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{status: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			f.calls.Add(1)
			f.lastPa = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			var p struct {
				ChatID any    `json:"chat_id"`
				Text   string `json:"text"`
			}
			_ = json.Unmarshal(body, &p)
			f.lastCh, f.lastTx = p.ChatID, p.Text
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		if f.okBody != "" {
			_, _ = io.WriteString(w, f.okBody)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newTestNotifier(t *testing.T, f *fakeTelegram, chatID string) *Telegram {
	t.Helper()
	n, err := NewTelegram(testToken, chatID, time.Second, bot.WithServerURL(f.srv.URL))
	require.NoError(t, err)
	return n
}

// A committed Status change yields exactly one Telegram message, addressed to
// the configured chat, carrying the Alert's text.
func TestTelegram_NotifySendsOneMessage(t *testing.T) {
	f := newFakeTelegram(t)
	n := newTestNotifier(t, f, "-1001234")

	err := n.Notify(context.Background(), core.Alert{
		Subject: "web", Status: core.StatusDown, Message: "web is now DOWN",
	})
	require.NoError(t, err)

	require.Equal(t, int64(1), f.calls.Load())
	require.True(t, strings.HasSuffix(f.lastPa, "/bot"+testToken+"/sendMessage"))
	require.EqualValues(t, -1001234, f.lastCh) // numeric chat id sent as a number
	require.Equal(t, "web is now DOWN", f.lastTx)
}

// A non-numeric chat id (a @channel username) is passed through as a string.
func TestTelegram_NotifyAcceptsChannelUsername(t *testing.T) {
	f := newFakeTelegram(t)
	n := newTestNotifier(t, f, "@my_channel")

	require.NoError(t, n.Notify(context.Background(), core.Alert{Subject: "web", Message: "x"}))
	require.Equal(t, "@my_channel", f.lastCh)
}

// An API failure surfaces as a wrapped error so the monitor logs and moves on
// (CONVENTIONS rule 10) — a dead Telegram never crashes the daemon.
func TestTelegram_NotifyWrapsAPIFailure(t *testing.T) {
	f := newFakeTelegram(t)
	f.okBody = `{"ok":false,"description":"chat not found"}`
	n := newTestNotifier(t, f, "42")

	err := n.Notify(context.Background(), core.Alert{Subject: "web", Message: "down"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "web")
}

// The bot token is a secret: it must never appear in a returned error
// (CONVENTIONS rule 8) even though it is part of the API URL path.
func TestTelegram_ErrorNeverLeaksToken(t *testing.T) {
	f := newFakeTelegram(t)
	f.status = 500
	f.okBody = "boom"
	n := newTestNotifier(t, f, "42")

	err := n.Notify(context.Background(), core.Alert{Subject: "web", Message: "down"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), testToken)
}

// An already-cancelled context stops the call before it hits the network
// (rule 4) and returns an error rather than blocking.
func TestTelegram_NotifyHonorsContext(t *testing.T) {
	f := newFakeTelegram(t)
	n := newTestNotifier(t, f, "42")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := n.Notify(ctx, core.Alert{Subject: "web", Message: "down"})
	require.Error(t, err)
	require.Equal(t, int64(0), f.calls.Load())
}

var _ core.Notifier = (*Telegram)(nil)

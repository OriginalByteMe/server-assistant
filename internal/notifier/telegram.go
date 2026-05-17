package notifier

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-telegram/bot"

	"server-assistant/internal/core"
)

// Telegram is the v1 one-way Notifier: it delivers each committed-Status
// Alert to the Operator's chat over the Telegram Bot API, behind the
// core.Notifier seam (CONTEXT.md: Alert is strictly one-way in v1; the M2
// two-way Approval is a separate seam). It never polls for updates.
type Telegram struct {
	api    *bot.Bot
	chatID any // int64 for a numeric chat, string for an @channel username
}

var _ core.Notifier = (*Telegram)(nil)

// NewTelegram builds a Telegram notifier. token and chatID are secrets from
// config (env-supplied, never logged — CONVENTIONS rule 7/8). timeout caps
// every delivery in addition to the caller's context (rule 4). Extra opts let
// tests redirect the API at a fake server; production passes none. getMe is
// skipped so construction performs no network I/O and the daemon starts even
// while Telegram is unreachable.
func NewTelegram(token, chatID string, timeout time.Duration, opts ...bot.Option) (*Telegram, error) {
	base := []bot.Option{
		bot.WithSkipGetMe(),
		bot.WithHTTPClient(timeout, &http.Client{Timeout: timeout}),
	}
	api, err := bot.New(token, append(base, opts...)...)
	if err != nil {
		// bot.New errors carry no token; still wrap with our own context.
		return nil, fmt.Errorf("telegram: init bot: %w", err)
	}
	return &Telegram{api: api, chatID: parseChatID(chatID)}, nil
}

// parseChatID keeps a numeric chat id typed as int64 (what the Bot API
// expects) while letting an @channel username pass through as a string.
func parseChatID(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return s
}

// Notify delivers one Alert as one Telegram message. A delivery failure is
// wrapped and returned (never panics — rule 10); the monitor logs it and the
// daemon keeps running. The bot token lives only in the API URL the library
// builds internally, never in the wrapped error (rule 8).
func (t *Telegram) Notify(ctx context.Context, a core.Alert) error {
	text := a.Message
	if text == "" {
		text = a.Subject + " is now " + a.Status.String()
	}
	if _, err := t.api.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: t.chatID,
		Text:   text,
	}); err != nil {
		return fmt.Errorf("telegram: notify %q: %w", a.Subject, err)
	}
	return nil
}

// Package notifier holds Notifier implementations. v1 issue 0001 ships only a
// stub; the Telegram notifier arrives in issue 0003.
package notifier

import (
	"context"
	"log/slog"

	"server-assistant/internal/core"
)

// Stub is a no-op Notifier that logs the Alert instead of delivering it.
type Stub struct{}

var _ core.Notifier = Stub{}

func (Stub) Notify(_ context.Context, a core.Alert) error {
	slog.Info("alert (stub notifier — not delivered)",
		"subject", a.Subject, "status", a.Status.String(), "message", a.Message)
	return nil
}

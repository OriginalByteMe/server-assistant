package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The rolling-retention window is configurable; omitted ⇒ a sane default so
// history never grows unbounded even with no explicit config (ADR 0002).
func TestLoad_HistoryWindowDefault(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, c.History.Window())
}

func TestLoad_HistoryWindowParsed(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nhistory:\n  window: \"6h\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, 6*time.Hour, c.History.Window())
}

func TestLoad_HistoryWindowRejectsBadDuration(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nhistory:\n  window: \"notaduration\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "history window")
}

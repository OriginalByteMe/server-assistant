package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestLoad_Valid(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nhttp_addr: \":9000\"\ndatabase:\n  path: \"x.db\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, c.SchemaVersion)
	require.Equal(t, ":9000", c.HTTPAddr)
	require.Equal(t, "x.db", c.Database.Path)
}

func TestLoad_DefaultsApplied(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, ":8080", c.HTTPAddr)
	require.Equal(t, "server-assistant.db", c.Database.Path)
}

func TestLoad_RejectsMissingVersion(t *testing.T) {
	p := writeTemp(t, "http_addr: \":8080\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "schema_version is required")
}

func TestLoad_RejectsUnsupportedVersion(t *testing.T) {
	p := writeTemp(t, "schema_version: 99\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported schema_version")
}

func TestLoad_ExpandsEnvSecret(t *testing.T) {
	t.Setenv("SA_TEST_SECRET", "topsecret")
	p := writeTemp(t, "schema_version: 1\ndatabase:\n  path: \"${SA_TEST_SECRET}.db\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "topsecret.db", c.Database.Path)
}

func TestLoad_RejectsUnsetEnvReference(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\ndatabase:\n  path: \"${SA_DEFINITELY_UNSET_VAR}.db\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unset environment variables")
	require.Contains(t, err.Error(), "SA_DEFINITELY_UNSET_VAR")
}

func TestLoad_ServiceParsedWithDefaults(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: web\n    url: \"https://example.test\"\n    latency_threshold: \"750ms\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Len(t, c.Services, 1)
	s := c.Services[0]
	require.Equal(t, "web", s.Name)
	require.Equal(t, 750*time.Millisecond, s.Threshold())
	require.Equal(t, 30*time.Second, s.Poll())         // default
	require.Equal(t, 10*time.Second, s.ProbeTimeout()) // default
	require.Equal(t, 3, s.DebounceN)                   // default
}

// A Service with no probe target is rejected. ARK-8 added tcp and ARK-13
// added container, so the requirement is now exactly one of url / tcp /
// container — never several, never none (rule 6).
func TestLoad_RejectsServiceWithoutURL(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: web\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one of url, tcp or container is required")
}

// A Service name is wired verbatim into the dashboard's HTML id and the
// vendored SSE extension's sse-swap event identifier; the extension parses
// sse-swap as a comma-separated list, so a comma in the name silently breaks
// live updates for that Service. Reject names carrying characters unsafe for
// that wiring at config load (rule 6: config is the source of truth) instead
// of shipping a half-broken dashboard.
func TestLoad_RejectsServiceNameWithUnsafeChars(t *testing.T) {
	for _, name := range []string{"plex, media", "a\nb", "a\rb", `a"b`, "a<b", "a>b"} {
		p := writeTemp(t, "schema_version: 1\nservices:\n  - name: "+yamlQuote(name)+"\n    url: \"https://x.test\"\n")
		_, err := NewFileSource(p).Load(context.Background())
		require.Error(t, err, "name %q must be rejected", name)
		require.Contains(t, err.Error(), "unsafe for dashboard wiring")
	}
}

// Human-friendly names (spaces, dashes, dots, underscores) stay valid — the
// rule only blocks the genuinely-breaking characters.
func TestLoad_AcceptsHumanFriendlyServiceName(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: \"Plex Media-Server_1.0\"\n    url: \"https://x.test\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Plex Media-Server_1.0", c.Services[0].Name)
}

func yamlQuote(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r")
	return "\"" + r.Replace(s) + "\""
}

func TestLoad_RejectsBadDuration(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nservices:\n  - name: web\n    url: \"https://x.test\"\n    poll_interval: \"notaduration\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "poll_interval")
}

// Telegram is optional: a config with no telegram block is valid and the
// notifier stays the Stub (main wiring), so Configured() must report false.
func TestLoad_TelegramAbsentIsValidAndUnconfigured(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.False(t, c.Telegram.Configured())
}

// The bot token and chat id are secrets: they live in env, referenced via
// ${VAR} (CONVENTIONS rule 7), and are expanded after parsing like every
// other secret-bearing field.
func TestLoad_TelegramExpandsEnvSecrets(t *testing.T) {
	t.Setenv("SA_TG_TOKEN", "123:abc")
	t.Setenv("SA_TG_CHAT", "-1009999")
	p := writeTemp(t, "schema_version: 1\ntelegram:\n  bot_token: \"${SA_TG_TOKEN}\"\n  chat_id: \"${SA_TG_CHAT}\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.True(t, c.Telegram.Configured())
	require.Equal(t, "123:abc", c.Telegram.BotToken)
	require.Equal(t, "-1009999", c.Telegram.ChatID)
}

// A ${VAR} the operator forgot to set is a hard error, never a silent empty
// token (consistent with the existing secret resolver).
func TestLoad_TelegramRejectsUnsetEnvReference(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\ntelegram:\n  bot_token: \"${SA_TG_DEFINITELY_UNSET}\"\n  chat_id: \"42\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unset environment variables")
	require.Contains(t, err.Error(), "SA_TG_DEFINITELY_UNSET")
}

// Half a config is a misconfiguration, not a silent half-on notifier: a token
// without a chat (or vice versa) is rejected at load (rule 6).
func TestLoad_TelegramRejectsPartialConfig(t *testing.T) {
	for _, body := range []string{
		"schema_version: 1\ntelegram:\n  bot_token: \"123:abc\"\n",
		"schema_version: 1\ntelegram:\n  chat_id: \"42\"\n",
	} {
		p := writeTemp(t, body)
		_, err := NewFileSource(p).Load(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "telegram")
		require.Contains(t, err.Error(), "bot_token")
		require.Contains(t, err.Error(), "chat_id")
	}
}

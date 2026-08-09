package mcp

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// doAuthRPC POSTs a fixed tools/list request through s.Handler(), optionally
// with the given raw Authorization header value, and returns the raw
// response so callers can assert on status, headers and body bytes — unlike
// rpcCall (server_test.go) this does not require a 200.
func doAuthRPC(t *testing.T, s *Server, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// withCapturedLogs swaps slog's default handler for the duration of fn and
// returns everything logged. Safe here because no test in this package uses
// t.Parallel().
func withCapturedLogs(fn func()) string {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

func TestAuth_NoTokenConfigured_ServesUnauthenticatedAndWarnsAtStartup(t *testing.T) {
	var s *Server
	logs := withCapturedLogs(func() {
		s = NewServer(&fakeSource{}, NoopProposalSink{}, ServerOptions{})
	})
	require.Contains(t, logs, "unauthenticated", "NewServer must WARN once at startup when no token is configured")

	rec := doAuthRPC(t, s, "")
	require.Equal(t, http.StatusOK, rec.Code, "no token configured means every request is served, including with no Authorization header")
}

func TestAuth_CorrectBearer_Succeeds(t *testing.T) {
	s := NewServer(&fakeSource{}, NoopProposalSink{}, ServerOptions{AuthToken: "s3cret-token"})
	rec := doAuthRPC(t, s, "Bearer s3cret-token")
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestAuth_MissingBearer_Returns401(t *testing.T) {
	s := NewServer(&fakeSource{}, NoopProposalSink{}, ServerOptions{AuthToken: "s3cret-token"})
	rec := doAuthRPC(t, s, "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuth_WrongBearer_Returns401ByteIdenticalToMissing(t *testing.T) {
	s := NewServer(&fakeSource{}, NoopProposalSink{}, ServerOptions{AuthToken: "s3cret-token"})
	missing := doAuthRPC(t, s, "")
	wrong := doAuthRPC(t, s, "Bearer definitely-not-it")

	require.Equal(t, http.StatusUnauthorized, missing.Code)
	require.Equal(t, http.StatusUnauthorized, wrong.Code)
	require.True(t, bytes.Equal(missing.Body.Bytes(), wrong.Body.Bytes()),
		"missing-token and wrong-token bodies must be byte-identical — the client must not be able to tell absent from wrong")
	require.Equal(t, missing.Header().Get("Content-Type"), wrong.Header().Get("Content-Type"))
}

func TestAuth_GetRequestAlsoRequiresBearer(t *testing.T) {
	s := NewServer(&fakeSource{}, NoopProposalSink{}, ServerOptions{AuthToken: "s3cret-token"})
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "auth is checked before the method allow-list")
}

func TestAuth_TokenNeverAppearsInLogs(t *testing.T) {
	const token = "super-secret-value-should-never-be-logged-xyz123"
	var s *Server
	logs := withCapturedLogs(func() {
		s = NewServer(&fakeSource{}, NoopProposalSink{}, ServerOptions{AuthToken: token})
		doAuthRPC(t, s, "Bearer "+token)
		doAuthRPC(t, s, "Bearer wrong-guess")
		doAuthRPC(t, s, "")
	})
	require.False(t, strings.Contains(logs, token), "the configured token must never appear in log output")
}

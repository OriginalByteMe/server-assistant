package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testLogger discards log output so test failures aren't buried in noise;
// swap in slog.Default() locally when debugging a failing case.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// runLines feeds input (already newline-joined) through run() against srv
// and returns exactly the lines written to stdout.
func runLines(t *testing.T, input string, srv *httptest.Server) []string {
	t.Helper()
	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := run(ctx, strings.NewReader(input), &stdout, testLogger(), srv.Client(), srv.URL, 2*time.Second, "")
	if err != nil {
		t.Fatalf("run() returned error: %v", err)
	}

	out := stdout.String()
	if out == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines
}

// runLinesWithToken is runLines but forwards token to run(), for the
// bearer-auth-specific cases below.
func runLinesWithToken(t *testing.T, input string, srv *httptest.Server, token string) []string {
	t.Helper()
	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := run(ctx, strings.NewReader(input), &stdout, testLogger(), srv.Client(), srv.URL, 2*time.Second, token)
	if err != nil {
		t.Fatalf("run() returned error: %v", err)
	}

	out := stdout.String()
	if out == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines
}

func TestRequestGetsResponseLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := jsonBody(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"ok":true}}`, string(body["id"]))
	}))
	defer srv.Close()

	lines := runLines(t, `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`+"\n", srv)
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 output line, got %d: %q", len(lines), lines)
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("output line is not valid JSON: %v", err)
	}
	if string(resp["id"]) != "1" {
		t.Errorf("id = %s, want 1", resp["id"])
	}
}

func TestNotificationProducesNoOutput(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	lines := runLines(t, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`+"\n", srv)
	if len(lines) != 0 {
		t.Fatalf("notification produced output: %q", lines)
	}
	if !called {
		t.Fatal("notification was never forwarded to upstream")
	}
}

func TestNotificationWithIDNullProducesNoOutput(t *testing.T) {
	// "id": null is the same "no correlation possible" signal as an
	// absent id for a client that isn't waiting on a reply.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	lines := runLines(t, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`+"\n", srv)
	if len(lines) != 0 {
		t.Fatalf("want no output, got %q", lines)
	}
}

func TestLargeResponseRoundTripsIntact(t *testing.T) {
	// Larger than bufio.MaxScanTokenSize (64KiB) — proves the shim doesn't
	// silently truncate a big tools/list-shaped response.
	padding := strings.Repeat("x", 200*1024)
	wantResult := fmt.Sprintf(`{"padding":"%s"}`, padding)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, wantResult)
	}))
	defer srv.Close()

	lines := runLines(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`+"\n", srv)
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 output line, got %d", len(lines))
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("output line did not parse (likely truncated): %v", err)
	}
	if string(resp.Result) != wantResult {
		t.Fatalf("result truncated or corrupted: got %d bytes, want %d", len(resp.Result), len(wantResult))
	}
}

func TestTransportErrorProducesWellFormedJSONRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	badURL := srv.URL
	srv.Close() // closed before use: every request now fails at the transport layer

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := run(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":"abc","method":"ping","params":{}}`+"\n"), &stdout, testLogger(), http.DefaultClient, badURL, 2*time.Second, "")
	if err != nil {
		t.Fatalf("run() returned error: %v", err)
	}

	line := strings.TrimRight(stdout.String(), "\n")
	if line == "" {
		t.Fatal("transport failure produced no output at all")
	}
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *rpcErrorObj    `json:"error"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("output is not valid JSON-RPC: %v (line: %q)", err, line)
	}
	if resp.Error == nil {
		t.Fatalf("want a JSON-RPC error object, got %q", line)
	}
	if resp.Error.Code == 0 || resp.Error.Message == "" {
		t.Errorf("error object incomplete: %+v", resp.Error)
	}
	if string(resp.ID) != `"abc"` {
		t.Errorf("id = %s, want \"abc\" (echoed from the request)", resp.ID)
	}
}

func TestNothingExtraneousOnStdoutAcrossMixedSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := jsonBody(r)
		if _, hasID := body["id"]; !hasID {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, string(body["id"]))
	}))
	defer srv.Close()

	input := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}` + "\n"
	lines := runLines(t, input, srv)
	if len(lines) != 1 {
		t.Fatalf("mixed session: want exactly 1 output line (the request's), got %d: %q", len(lines), lines)
	}
}

func jsonBody(r *http.Request) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func TestNoToken_NoAuthorizationHeaderSent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	}))
	defer srv.Close()

	runLines(t, `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`+"\n", srv)
	if gotAuth != "" {
		t.Errorf("Authorization header sent with no token configured: %q", gotAuth)
	}
}

func TestTokenConfigured_SendsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	}))
	defer srv.Close()

	runLinesWithToken(t, `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`+"\n", srv, "s3cret-shim-token")
	if want := "Bearer s3cret-shim-token"; gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

func TestUpstream401_ProducesWellFormedJSONRPCErrorOnStdout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"unauthorized: missing or invalid bearer token"}}`)
	}))
	defer srv.Close()

	lines := runLinesWithToken(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`+"\n", srv, "wrong-token")
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 output line, got %d: %q", len(lines), lines)
	}
	var resp struct {
		Error *rpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("output is not valid JSON-RPC: %v (line: %q)", err, lines[0])
	}
	if resp.Error == nil {
		t.Fatalf("want a JSON-RPC error object for a 401 upstream response, got %q", lines[0])
	}
}

// TestTokenNeverAppearsInShimLogsOrStdout exercises both the
// header-attached success path and the upstream-401-error-logging path
// (handleLine logs the response status/body on non-2xx) with a real
// configured token, and proves it never leaks onto stderr (the logger) or
// stdout.
func TestTokenNeverAppearsInShimLogsOrStdout(t *testing.T) {
	const token = "super-secret-shim-token-should-never-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"unauthorized: missing or invalid bearer token"}}`)
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`+"\n"), &stdout, logger, srv.Client(), srv.URL, 2*time.Second, token); err != nil {
		t.Fatalf("run() returned error: %v", err)
	}

	if strings.Contains(logBuf.String(), token) {
		t.Errorf("token leaked into shim logs: %s", logBuf.String())
	}
	if strings.Contains(stdout.String(), token) {
		t.Errorf("token leaked onto stdout: %s", stdout.String())
	}
}

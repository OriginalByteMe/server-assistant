package reasoner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

func TestDiagnoseHappyPath(t *testing.T) {
	// Happy path: backend returns valid chat-completion with action, subject,
	// rationale, summary, and usage metrics.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer secret123", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": `{"action":"restart_container","subject":"database","rationale":"high memory","summary":"The database service is consuming excessive memory and needs to be restarted."}`,
					},
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 20,
			},
		})
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "secret123",
		Secrets: []string{},
	}
	c := New(cfg)
	reply, err := c.Diagnose(context.Background(), "test prompt")
	require.NoError(t, err)
	require.Equal(t, core.ActionRestartContainer, reply.Action)
	require.Equal(t, "database", reply.Subject)
	require.Equal(t, "high memory", reply.Rationale)
	require.Equal(t, "The database service is consuming excessive memory and needs to be restarted.", reply.Summary)
	require.Equal(t, "test-model", reply.Usage.Model)
	require.Equal(t, 10, reply.Usage.PromptTokens)
	require.Equal(t, 20, reply.Usage.CompletionTokens)
	require.Greater(t, reply.Usage.Latency, time.Duration(0))
}

func TestDiagnoseFencedJSON(t *testing.T) {
	// Fenced and prose-wrapped JSON is still parsed correctly.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `The model recommends the following action:
` + "```json" + `
{"action":"none","subject":"","rationale":"","summary":"Everything looks fine."}
` + "```",
					},
				},
			},
			"usage": map[string]interface{}{},
		})
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "",
		Secrets: []string{},
	}
	c := New(cfg)
	reply, err := c.Diagnose(context.Background(), "test prompt")
	require.NoError(t, err)
	require.Equal(t, core.ActionNone, reply.Action)
}

func TestDiagnoseInvalidAction(t *testing.T) {
	// Invalid action value returns an error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": `{"action":"bad_action","subject":"","rationale":"","summary":""}`,
					},
				},
			},
			"usage": map[string]interface{}{},
		})
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "",
		Secrets: []string{},
	}
	c := New(cfg)
	_, err := c.Diagnose(context.Background(), "test prompt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid action")
}

func TestDiagnose500Error(t *testing.T) {
	// Non-2xx response returns an error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "",
		Secrets: []string{},
	}
	c := New(cfg)
	_, err := c.Diagnose(context.Background(), "test prompt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "status")
}

func TestDiagnoseScrubFailure(t *testing.T) {
	// Scrub failure (1-char secret) returns error and makes ZERO HTTP requests.
	requestCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "",
		Secrets: []string{"x"}, // 1-char secret triggers rejection
	}
	c := New(cfg)
	_, err := c.Diagnose(context.Background(), "test prompt")
	require.Error(t, err)
	require.True(t, errors.Is(err, core.ErrScrubFailed) || strings.Contains(err.Error(), core.ErrScrubFailed.Error()),
		"error should wrap or mention ErrScrubFailed, got: %v", err)
	require.Equal(t, int32(0), requestCount.Load(), "no HTTP request should be made on scrub failure")
}

func TestDiagnoseAuthorizationHeader(t *testing.T) {
	// Authorization header is set correctly with the API key.
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": `{"action":"none","subject":"","rationale":"","summary":""}`,
					},
				},
			},
			"usage": map[string]interface{}{},
		})
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "myapikey",
		Secrets: []string{},
	}
	c := New(cfg)
	_, err := c.Diagnose(context.Background(), "test prompt")
	require.NoError(t, err)
	require.Equal(t, "Bearer myapikey", capturedAuth)
}

func TestDiagnoseRationaleTruncation(t *testing.T) {
	// Rationale is truncated to maxRationaleRunes (300).
	longRationale := strings.Repeat("a", 500) // 500 runes
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": map[string]string{
							"action":    "none",
							"subject":   "",
							"rationale": longRationale,
							"summary":   "",
						},
					},
				},
			},
			"usage": map[string]interface{}{},
		})
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "",
		Secrets: []string{},
	}

	// Manually encode the JSON to match what the server would send
	server.Client().CloseIdleConnections()
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": `{"action":"none","subject":"","rationale":"` + longRationale + `","summary":""}`,
					},
				},
			},
			"usage": map[string]interface{}{},
		})
	}))
	cfg.BaseURL = server.URL
	c := New(cfg)
	reply, err := c.Diagnose(context.Background(), "test prompt")
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(reply.Rationale)), maxRationaleRunes)
}

func TestDiagnoseSummaryTruncation(t *testing.T) {
	// Summary is truncated to maxSummaryRunes (1000).
	longSummary := strings.Repeat("a", 2000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": `{"action":"none","subject":"","rationale":"","summary":"` + longSummary + `"}`,
					},
				},
			},
			"usage": map[string]interface{}{},
		})
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "",
		Secrets: []string{},
	}
	c := New(cfg)
	reply, err := c.Diagnose(context.Background(), "test prompt")
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(reply.Summary)), maxSummaryRunes)
}

func TestHealthy200(t *testing.T) {
	// Healthy succeeds on 200.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/models", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "",
		Secrets: []string{},
	}
	c := New(cfg)
	err := c.Healthy(context.Background())
	require.NoError(t, err)
}

func TestHealthy503(t *testing.T) {
	// Healthy fails on 503.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := Config{
		BaseURL: server.URL,
		Model:   "test-model",
		APIKey:  "",
		Secrets: []string{},
	}
	c := New(cfg)
	err := c.Healthy(context.Background())
	require.Error(t, err)
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "plain JSON",
			input: `{"action":"none","subject":"test"}`,
			want:  `{"action":"none","subject":"test"}`,
		},
		{
			name:  "fenced JSON",
			input: "```json\n{\"action\":\"none\",\"subject\":\"test\"}\n```",
			want:  `{"action":"none","subject":"test"}`,
		},
		{
			name:  "prose with fenced JSON",
			input: "The answer is:\n```json\n{\"action\":\"restart_container\",\"subject\":\"db\"}\n```\nDone.",
			want:  `{"action":"restart_container","subject":"db"}`,
		},
		{
			name:  "nested braces in string",
			input: `{"data":"{nested}"}`,
			want:  `{"data":"{nested}"}`,
		},
		{
			name:  "escaped quotes",
			input: `{"text":"say \"hello\""}`,
			want:  `{"text":"say \"hello\""}`,
		},
		{
			name:    "no JSON object",
			input:   `not a json`,
			wantErr: true,
		},
		{
			name:    "unbalanced braces",
			input:   `{"unclosed": {`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractJSON(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{
			name: "no truncation needed",
			s:    "hello",
			n:    10,
			want: "hello",
		},
		{
			name: "truncate to exact length",
			s:    "hello",
			n:    5,
			want: "hello",
		},
		{
			name: "truncate shorter",
			s:    "hello",
			n:    3,
			want: "hel",
		},
		{
			name: "unicode runes",
			s:    "你好世界",
			n:    2,
			want: "你好",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRunes(tt.s, tt.n)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestClientName(t *testing.T) {
	c := New(Config{Model: "qwen2.5:1.5b-instruct"})
	require.Equal(t, "openai-compatible:qwen2.5:1.5b-instruct", c.Name())
}

func TestNew(t *testing.T) {
	// Timeout defaults to 60s when zero.
	c := New(Config{})
	require.Equal(t, defaultTimeout, c.cfg.Timeout)

	// Non-zero timeout is preserved.
	cfg := Config{Timeout: 30 * time.Second}
	c = New(cfg)
	require.Equal(t, 30*time.Second, c.cfg.Timeout)
}

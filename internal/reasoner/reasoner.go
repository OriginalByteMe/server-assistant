// Package reasoner implements the M2 inference seam (core.Reasoner) against
// any OpenAI-compatible chat-completions API, using stdlib net/http only
// (CONVENTIONS rule 1). The default deployment target is a local Ollama
// instance so no evidence ever leaves the host (ADR 0013); a Cloud backend is
// an explicit opt-in the config layer gates, not this package.
//
// This package owns exactly one thing: turn a scrubbed prompt into a
// core.ReasonerReply, or fail. It does not resolve Service->container names
// (ADR 0018, harness-owned), does not execute anything (ADR 0012), and does
// not implement the deterministic runbook fallback (harness-owned).
package reasoner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"server-assistant/internal/core"
)

// Config configures a Client. Secrets is the literal-value scrub list passed
// to core.Scrub before every prompt leaves the process (ADR 0013).
type Config struct {
	BaseURL string
	Model   string
	APIKey  string
	Timeout time.Duration
	Secrets []string
}

const defaultTimeout = 60 * time.Second

// Client is an OpenAI-compatible chat-completions Reasoner.
type Client struct {
	cfg  Config
	http *http.Client
}

var _ core.Reasoner = (*Client)(nil)

// New builds a Client. Timeout defaults to 60s when zero.
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
	}
}

// Name identifies the backend for core.Usage.Backend and audit records. It
// never includes the API key.
func (c *Client) Name() string {
	return "openai-compatible:" + c.cfg.Model
}

// systemPrompt pins the exact output contract the model must follow. It is
// deliberately strict: anything the model returns outside this contract is
// treated as garbage and rejected (ADR 0009 fail-closed).
const systemPrompt = `You are a bounded, read-only diagnosis assistant for a homelab server monitor. You may only choose between two actions: "restart_container" or "none". You never name a container, host, command, or file path — only a Service name given to you in the prompt. Respond with exactly one JSON object and nothing else, matching this shape:
{"action":"restart_container"|"none","subject":"<service name>","rationale":"<one line>","summary":"<one short paragraph>"}`

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

// modelReply is the strict JSON contract the model must answer with.
type modelReply struct {
	Action    string `json:"action"`
	Subject   string `json:"subject"`
	Rationale string `json:"rationale"`
	Summary   string `json:"summary"`
}

const (
	maxRationaleRunes = 300
	maxSummaryRunes   = 1000
)

// Diagnose scrubs the prompt, calls the chat-completions endpoint, and parses
// the model's reply into a core.ReasonerReply. It fails closed: any scrub
// failure, transport error, non-2xx response, unparsable body, or invalid
// action returns an error and no HTTP request is made (for scrub failures) or
// no reply is produced (for the rest).
func (c *Client) Diagnose(ctx context.Context, prompt string) (core.ReasonerReply, error) {
	// Mandatory provider-independent scrub point (ADR 0013). Nothing is sent
	// until this succeeds.
	scrubbed, err := core.Scrub(prompt, c.cfg.Secrets)
	if err != nil {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: %w", err)
	}

	reqBody := chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: scrubbed},
		},
		ResponseFormat: &responseFormat{Type: "json_object"},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: encode request: %w", err)
	}

	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: read response: %w", err)
	}
	latency := time.Since(start)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: backend returned status %d", resp.StatusCode)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return core.ReasonerReply{}, errors.New("reasoner: backend returned no choices")
	}

	object, err := extractJSON(chatResp.Choices[0].Message.Content)
	if err != nil {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: extract reply: %w", err)
	}
	var reply modelReply
	if err := json.Unmarshal([]byte(object), &reply); err != nil {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: parse reply: %w", err)
	}

	if reply.Action != core.ActionRestartContainer && reply.Action != core.ActionNone {
		return core.ReasonerReply{}, fmt.Errorf("reasoner: invalid action %q", reply.Action)
	}

	return core.ReasonerReply{
		Action:    reply.Action,
		Subject:   reply.Subject,
		Rationale: truncateRunes(reply.Rationale, maxRationaleRunes),
		Summary:   truncateRunes(reply.Summary, maxSummaryRunes),
		Usage: core.Usage{
			Backend:          c.Name(),
			Model:            c.cfg.Model,
			PromptTokens:     chatResp.Usage.PromptTokens,
			CompletionTokens: chatResp.Usage.CompletionTokens,
			Latency:          latency,
		},
	}, nil
}

// Healthy reports whether the backend is reachable, for harness
// self-monitoring (ADR 0015). It never mutates anything.
func (c *Client) Healthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("reasoner: build health request: %w", err)
	}
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("reasoner: health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("reasoner: health check status %d", resp.StatusCode)
	}
	return nil
}

// extractJSON returns the first balanced top-level {...} object in s,
// tolerating surrounding prose or ```json fences. It tracks string literals
// and escapes so braces inside string values do not confuse the scan.
func extractJSON(s string) (string, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", errors.New("no JSON object found")
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", errors.New("unbalanced JSON object")
}

// truncateRunes truncates s to at most n runes, rune-safe.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

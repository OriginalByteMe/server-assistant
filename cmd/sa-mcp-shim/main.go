// Command sa-mcp-shim is the local stdio↔Streamable-HTTP bridge settled by
// issue #56 as the private fallback transport (HL-SA-20): it runs on the
// user's own machine, speaks MCP's stdio transport to a local client (e.g.
// Claude Desktop's local server config), and relays each JSON-RPC message to
// the real MCP endpoint on the Unraid box over the tailnet or LAN. No inbound
// port opens on the user's machine and Tailscale Funnel is never involved.
//
// Wire contract, verified against the MCP spec
// (https://modelcontextprotocol.io/specification/2025-11-25/basic/transports,
// "stdio" section) rather than written from memory:
//   - Messages are newline-delimited JSON-RPC 2.0 objects on stdin/stdout;
//     a message MUST NOT contain an embedded newline.
//   - stdout is reserved exclusively for protocol messages — nothing else,
//     ever, may land there. Logging/diagnostics go to stderr only.
//   - A notification (no "id" member) gets no response; the shim must not
//     emit a line for one, matching how internal/mcp/server.go itself
//     answers a notification with a bodyless 202 rather than a JSON-RPC
//     response.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	defaultEndpoint = "http://100.90.134.29:8099/mcp"
	defaultTimeout  = 30 * time.Second

	// scannerBufferCap bounds how large a single stdin line the shim will
	// buffer. The stdlib bufio.Scanner default token cap (64KiB) truncates
	// anything bigger — a real tools/list or tools/call payload can exceed
	// that — so this is raised well past any legitimate MCP message.
	scannerBufferCap = 10 * 1024 * 1024

	// JSON-RPC 2.0 reserved server-error range (-32000 to -32099); this
	// shim is the "server" from the local client's point of view whenever
	// it has to answer for the real server's absence.
	codeUpstreamError = -32000
)

// rpcErrorObj is the JSON-RPC 2.0 error member.
type rpcErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcErrorResponse is a full JSON-RPC 2.0 error response envelope, built
// locally (never through internal/mcp, which this command does not import)
// whenever the shim itself has to answer for a transport failure.
type rpcErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcErrorObj     `json:"error"`
}

func main() {
	endpoint := flag.String("endpoint", envOr("SA_MCP_ENDPOINT", defaultEndpoint), "MCP HTTP endpoint to relay to")
	timeout := flag.Duration("timeout", defaultTimeout, "per-request HTTP timeout")
	token := flag.String("token", envOr("SA_MCP_TOKEN", ""), "bearer token sent as Authorization: Bearer <token> (or SA_MCP_TOKEN); omit if the endpoint is unauthenticated")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{}
	// Log only whether a token is configured, never its value (CONVENTIONS
	// rule 8 — HL-SA-17).
	logger.Info("sa-mcp-shim starting", "endpoint", *endpoint, "timeout", timeout.String(), "auth_configured", *token != "")

	if err := run(ctx, os.Stdin, os.Stdout, logger, client, *endpoint, *timeout, *token); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
	logger.Info("sa-mcp-shim exiting")
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// run drives the core stdio<->HTTP loop. It is the unit under test: stdin,
// stdout and the HTTP client are all injected so tests never touch a real
// process or network.
func run(ctx context.Context, stdin io.Reader, stdout io.Writer, logger *slog.Logger, client *http.Client, endpoint string, timeout time.Duration, token string) error {
	lines := make(chan string)
	scanDone := make(chan error, 1)

	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 0, 64*1024), scannerBufferCap)
		for sc.Scan() {
			line := sc.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			select {
			case lines <- line:
			case <-ctx.Done():
				scanDone <- ctx.Err()
				return
			}
		}
		scanDone <- sc.Err()
	}()

	out := bufio.NewWriter(stdout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				return <-scanDone
			}
			handleLine(ctx, line, out, logger, client, endpoint, timeout, token)
			if err := out.Flush(); err != nil {
				return fmt.Errorf("flush stdout: %w", err)
			}
		}
	}
}

// handleLine relays one JSON-RPC message. It never returns an error to the
// caller: every failure mode either produces a JSON-RPC error line (for a
// request the client is waiting on) or a stderr log entry (for a
// notification, which gets no response either way).
func handleLine(ctx context.Context, line string, out *bufio.Writer, logger *slog.Logger, client *http.Client, endpoint string, timeout time.Duration, token string) {
	id, isNotification, parseErr := extractID(line)
	if parseErr != nil {
		// Malformed JSON: per JSON-RPC 2.0, a parse error response always
		// carries id: null, since the id (if any) could not be reliably
		// read out of unparseable text. No point forwarding garbage.
		logger.Error("stdin line is not valid JSON", "err", parseErr)
		writeError(out, logger, json.RawMessage("null"), codeUpstreamError, "shim: invalid JSON-RPC message: "+parseErr.Error())
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader([]byte(line)))
	if err != nil {
		logger.Error("build request failed", "err", err)
		if !isNotification {
			writeError(out, logger, id, codeUpstreamError, "shim: could not build upstream request: "+err.Error())
		}
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		// Never logged — only ever placed on the outbound request
		// (CONVENTIONS rule 8, HL-SA-17).
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		logger.Error("upstream request failed", "endpoint", endpoint, "err", err)
		if !isNotification {
			writeError(out, logger, id, codeUpstreamError, "shim: upstream request failed: "+err.Error())
		}
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("reading upstream response failed", "err", err)
		if !isNotification {
			writeError(out, logger, id, codeUpstreamError, "shim: reading upstream response failed: "+err.Error())
		}
		return
	}

	if isNotification {
		// The client sent a notification and expects nothing back,
		// regardless of what the HTTP transport did with it.
		if resp.StatusCode >= 300 {
			logger.Warn("notification got a non-2xx upstream status", "status", resp.StatusCode)
		}
		return
	}

	if resp.StatusCode >= 300 {
		logger.Error("upstream returned non-2xx", "status", resp.StatusCode, "body", string(body))
		writeError(out, logger, id, codeUpstreamError, fmt.Sprintf("shim: upstream returned HTTP %d", resp.StatusCode))
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		logger.Error("upstream returned an empty body for a request expecting a response")
		writeError(out, logger, id, codeUpstreamError, "shim: upstream returned an empty response")
		return
	}

	writeLine(out, logger, body)
}

// extractID reports whether line carries a top-level "id" key (JSON-RPC 2.0
// notifications omit it) and its raw value when present, without losing
// numeric precision or forwarding through a lossy interface{} round-trip.
func extractID(line string) (id json.RawMessage, isNotification bool, err error) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil, false, err
	}
	raw, ok := msg["id"]
	if !ok {
		return nil, true, nil
	}
	return raw, false, nil
}

// writeLine emits exactly one protocol line: the given body (already a
// complete JSON-RPC message from the upstream server), stripped of any
// trailing whitespace/newline and terminated by exactly one '\n'. Never
// writes to anything but out.
func writeLine(out *bufio.Writer, logger *slog.Logger, body []byte) {
	body = bytes.TrimRight(body, "\r\n \t")
	if _, err := out.Write(body); err != nil {
		logger.Error("writing stdout failed", "err", err)
		return
	}
	if err := out.WriteByte('\n'); err != nil {
		logger.Error("writing stdout newline failed", "err", err)
	}
}

func writeError(out *bufio.Writer, logger *slog.Logger, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, err := json.Marshal(rpcErrorResponse{JSONRPC: "2.0", ID: id, Error: rpcErrorObj{Code: code, Message: message}})
	if err != nil {
		// Marshalling a struct of static-shape fields cannot fail in
		// practice; if it somehow does, there is nothing left to do but
		// log it — writing broken JSON to stdout would be worse than
		// writing nothing.
		logger.Error("marshalling JSON-RPC error response failed", "err", err)
		return
	}
	writeLine(out, logger, b)
}

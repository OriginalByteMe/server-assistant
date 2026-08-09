// Package mcp implements the stateless MCP (Model Context Protocol) surface
// for the user's own LLM: a single JSON-RPC 2.0 endpoint over Streamable
// HTTP (https://modelcontextprotocol.io/specification/2025-11-25), built on
// stdlib net/http and encoding/json only (CONVENTIONS rule 1 — no MCP SDK).
//
// "Stateless" here means: no session ID, no server-initiated SSE stream, no
// server->client requests. Every POST is answered with one JSON object in
// the same request/response cycle — the spec explicitly permits this: "the
// server MUST either return Content-Type: text/event-stream... or
// application/json, to return one JSON object"
// (basic/transports#sending-messages-to-the-server). A GET to the endpoint
// gets the spec's other permitted answer, 405, since we offer no SSE stream
// (basic/transports#listening-for-messages-from-the-server, point 3: "The
// server MUST either return Content-Type: text/event-stream... or else
// return HTTP 405 Method Not Allowed").
package mcp

import "encoding/json"

// protocolVersion is the MCP revision this server speaks. 2025-11-25 is the
// latest finalized (non-RC) revision as of writing — 2026-07-28 exists only
// as a release candidate ("this specification is not final", per the
// project's own release notes) and was deliberately not targeted.
const protocolVersion = "2025-11-25"

// rpcRequest is the client->server envelope
// (specification/2025-11-25/basic#requests). ID is carried as raw JSON so a
// string, a number, or an absent field (a notification, per
// basic#notifications: "Notifications MUST NOT include an ID") all
// round-trip without modelling the id: string|number union by hand.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is the server->client envelope. Result and Error are mutually
// exclusive per spec: "Either a result or an error MUST be set. A response
// MUST NOT set both."
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC 2.0 codes, plus the range MCP itself uses for
// catalog-lookup refusals. -32000..-32099 is reserved by JSON-RPC 2.0 for
// "implementation-defined server-errors"; MCP's own resources spec places
// "Resource not found" at exactly -32002
// (server/resources#error-handling) — that is the concrete instance of the
// range this server issues, for resources/read on an unknown URI.
//
// Tool *execution* failures (a known tool that fails to do what it was
// asked — an unauthenticated Unraid API, a disk in standby, a proposal
// sink that isn't wired up yet) are deliberately NOT reported through this
// range or any JSON-RPC error. MCP's tools spec requires those inside a
// normal, successful CallToolResult with isError:true instead, precisely
// so the LLM sees the reason in-band and can self-correct
// (server/tools#error-handling: "Tool Execution Errors ... contain
// actionable feedback that language models can use to self-correct").
// Only a malformed call or an unknown tool name are protocol errors here —
// see ToolResult and Server.handleToolsCall.
const (
	codeParseError       = -32700
	codeInvalidRequest   = -32600
	codeMethodNotFound   = -32601
	codeInvalidParams    = -32602
	codeInternalError    = -32603
	codeResourceNotFound = -32002
)

package mcp

import (
	"context"
	"encoding/json"
)

// Tool is one MCP tool: a parameterised query exposed to the user's LLM
// (specification/2025-11-25/server/tools#tool). Category has no protocol
// home of its own, so it travels in the tool's _meta object — a mechanism
// the spec reserves for exactly this kind of server-defined extension
// (specification/2025-11-25/basic#_meta) — so tools/list reads as an
// organised, categorised surface rather than a flat dump (HL-SA-17,
// coordinator decision B1).
type Tool struct {
	Name        string
	Category    string
	Description string
	// InputSchema is a JSON Schema object per the spec's tool-schema
	// guidance. Nil is rendered as the spec's recommended empty-input
	// schema: {"type":"object","additionalProperties":false}.
	InputSchema map[string]any
	// Required names the argument keys tools/call MUST receive. A missing
	// key is a malformed call (-32602), checked before Handler ever runs —
	// this is dispatch validation, not a tool execution error.
	Required    []string
	Annotations Annotations
	Handler     ToolHandler
}

// Annotations describes tool behaviour to well-behaved clients — nothing
// more. The spec is explicit: "For trust & safety and security, clients
// MUST consider tool annotations to be untrusted unless they come from
// trusted servers" (server/tools#data-types, Tool's Warning box). A
// DestructiveHint:true tool that a client chooses to ignore the hint on
// still MUST be refused server-side if it isn't allowed — see
// ProposalSink. Annotations are a courtesy; enforcement never lives here.
type Annotations struct {
	ReadOnlyHint    bool
	DestructiveHint bool
	IdempotentHint  bool
	OpenWorldHint   bool
}

// ToolResult is what a tools/call returns on success — including a known
// business-level failure. IsError marks the spec's "Tool Execution Error"
// path (server/tools#error-handling): API failures, validation errors,
// business-logic refusals. It is never used for a malformed call or an
// unknown tool name, both of which are JSON-RPC protocol errors returned
// before Handler ever runs (see Server.handleToolsCall).
type ToolResult struct {
	Content string
	IsError bool
}

// ToolHandler executes one tool call. args is the parsed `arguments`
// object; detail is the B2 opt-in ("every read tool returns a summary by
// default with an explicit detail:true opt-in"). A returned error is
// wrapped into ToolResult{IsError:true} automatically by the dispatcher —
// handlers that want a structured, LLM-actionable message should build one
// with structuredError instead of returning a bare error.
type ToolHandler func(ctx context.Context, args map[string]any, detail bool) (ToolResult, error)

// toolErrorBody is the structured shape behind a ToolResult.IsError text
// payload (coordinator decision B4: "code, one-line reason, allowed
// alternatives" — so the LLM can correct itself without learning anything
// it shouldn't).
type toolErrorBody struct {
	Code         string   `json:"code"`
	Reason       string   `json:"reason"`
	Alternatives []string `json:"alternatives,omitempty"`
}

func structuredError(code, reason string, alternatives ...string) ToolResult {
	b, _ := json.Marshal(toolErrorBody{Code: code, Reason: reason, Alternatives: alternatives})
	return ToolResult{Content: string(b), IsError: true}
}

// InvalidParamsError marks a Handler validation failure that must surface
// as a genuine JSON-RPC "Invalid params" protocol error (-32602) rather
// than a Tool Execution Error — for malformed arguments a JSON Schema
// "required" check cannot express, e.g. get_smart_trend's hours/days
// window (HL-SA-19). Business-logic refusals (unknown series, disk
// standby, unauthenticated) stay Tool Execution Errors via
// structuredError — they ran, they just failed; this is for calls that
// never should have run as given.
type InvalidParamsError struct{ Message string }

func (e *InvalidParamsError) Error() string { return e.Message }

func boolArg(args map[string]any, name string) bool {
	v, _ := args[name].(bool)
	return v
}

func stringArg(args map[string]any, name string) string {
	v, _ := args[name].(string)
	return v
}

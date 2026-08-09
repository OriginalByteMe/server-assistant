package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"server-assistant/internal/core"
)

// maxRequestBytes bounds a single JSON-RPC request body. Any legitimate
// call here is a handful of scalar arguments — a body anywhere near this
// size is either abuse or a bug, not a real MCP call.
const maxRequestBytes = 64 * 1024

// Server is the stateless MCP surface: a JSON-RPC 2.0 dispatcher over one
// Streamable HTTP endpoint (see the package doc for what "stateless" means
// here). It carries no mutable per-connection state, so one *Server safely
// serves every request through Handler().
//
// Security note: the spec's Streamable HTTP section requires validating
// the Origin header against DNS-rebinding attacks, which target a
// localhost-bound dev server reached by a malicious page in the victim's
// own browser. This deployment's threat model differs — the server is
// reached over the tailnet or a Funnel HTTPS hostname by a programmatic
// LLM client (Claude's cloud connector, or a local stdio-shim), not by
// arbitrary browser JS with a spoofable Origin — so no allow-list exists
// yet.
// ponytail: add Origin/CORS enforcement if/when a browser-hosted MCP
// client is ever a real caller of this endpoint.
type Server struct {
	source           core.UnraidSource
	sink             ProposalSink
	dashboardBaseURL string
	tools            map[string]Tool
	toolOrder        []string
	resources        map[string]Resource
	resourceOrder    []string
}

// NewServer builds the MCP surface and registers every built-in tool and
// resource (HL-SA-17) against source. sink is the mutating-call seam (B3)
// — pass NoopProposalSink{} until HL-SA-18's grant model is wired in.
// dashboardBaseURL is stitched into the initialize handshake's
// instructions and get_proposal's not-configured message; empty is valid
// and simply omits both.
func NewServer(source core.UnraidSource, sink ProposalSink, dashboardBaseURL string) *Server {
	s := &Server{
		source:           source,
		sink:             sink,
		dashboardBaseURL: dashboardBaseURL,
		tools:            map[string]Tool{},
		resources:        map[string]Resource{},
	}
	registerHostTools(s, source)
	registerStorageTools(s, source)
	registerContainerTools(s, source)
	registerProposalTools(s, sink, dashboardBaseURL)
	registerBuiltinResources(s, source)
	return s
}

// Register adds or replaces one tool. Exported so a later ticket's
// mutating tools attach without this package growing new registration
// entry points — same seam as every read tool above.
func (s *Server) Register(t Tool) {
	if _, exists := s.tools[t.Name]; !exists {
		s.toolOrder = append(s.toolOrder, t.Name)
	}
	s.tools[t.Name] = t
}

// RegisterResource adds or replaces one resource.
func (s *Server) RegisterResource(r Resource) {
	if _, exists := s.resources[r.URI]; !exists {
		s.resourceOrder = append(s.resourceOrder, r.URI)
	}
	s.resources[r.URI] = r
}

// Handler returns the MCP endpoint. The caller mounts it at whatever path
// it chooses — the spec's own example uses "/mcp" — this package has no
// opinion on the mount point.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Streamable HTTP requires the endpoint to accept GET too, but
		// explicitly permits answering "no SSE stream here" with 405
		// instead
		// (basic/transports#listening-for-messages-from-the-server, point
		// 3) — the right answer for a stateless server with no
		// server-initiated stream to offer.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.handlePost(w, r)
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(body) > maxRequestBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, nil, codeParseError, "parse error: "+err.Error())
		return
	}
	if req.Method == "" {
		s.writeError(w, req.ID, codeInvalidRequest, "missing method")
		return
	}

	// A JSON-RPC notification (e.g. notifications/initialized) carries no
	// id and gets no response body — just 202 Accepted
	// (basic/transports#sending-messages-to-the-server, point 4). A
	// stateless server has no session for the notification to attach to,
	// so acknowledging it is the entire job here.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, rpcErr := s.dispatch(r.Context(), req.Method, req.Params)
	w.Header().Set("Content-Type", "application/json")
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *rpcError) {
	switch method {
	case "initialize":
		return s.handleInitialize()
	case "ping":
		return json.RawMessage(`{}`), nil
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(ctx, params)
	case "resources/list":
		return s.handleResourcesList()
	case "resources/read":
		return s.handleResourcesRead(ctx, params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}
	}
}

// handleInitialize always answers with protocolVersion (MCP's version
// negotiation lets the server respond with whatever version it supports,
// regardless of the client's requested version —
// basic/lifecycle#version-negotiation) and declares only the
// tools/resources capabilities this build actually offers
// (basic/lifecycle#capability-negotiation). It ignores the request body:
// nothing in it changes what this stateless server can do.
func (s *Server) handleInitialize() (json.RawMessage, *rpcError) {
	result := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "server-assistant",
			"version": "0.1.0",
		},
	}
	if s.dashboardBaseURL != "" {
		result["instructions"] = "Mutating actions are not implemented yet; the dashboard at " + s.dashboardBaseURL + " is read-only alongside this endpoint."
	}
	b, err := json.Marshal(result)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	return b, nil
}

func (s *Server) handleToolsList() (json.RawMessage, *rpcError) {
	tools := make([]map[string]any, 0, len(s.toolOrder))
	for _, name := range s.toolOrder {
		t := s.tools[name]
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "additionalProperties": false}
		}
		tools = append(tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
			"annotations": map[string]any{
				"readOnlyHint":    t.Annotations.ReadOnlyHint,
				"destructiveHint": t.Annotations.DestructiveHint,
				"idempotentHint":  t.Annotations.IdempotentHint,
				"openWorldHint":   t.Annotations.OpenWorldHint,
			},
			// _meta is the spec's own reserved-for-extension mechanism
			// (basic#_meta); category has no native Tool field, so it
			// travels here rather than as an invented top-level key.
			"_meta": map[string]any{"category": t.Category},
		})
	}
	b, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	return b, nil
}

func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (json.RawMessage, *rpcError) {
	var req struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &req); err != nil || req.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: a tool name is required"}
	}
	tool, ok := s.tools[req.Name]
	if !ok {
		// Matches the spec's own worked example verbatim
		// (server/tools#error-handling): "Unknown tool: invalid_tool_name".
		return nil, &rpcError{Code: codeInvalidParams, Message: "Unknown tool: " + req.Name}
	}

	args := map[string]any{}
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid arguments: " + err.Error()}
		}
	}
	for _, name := range tool.Required {
		if _, present := args[name]; !present {
			return nil, &rpcError{Code: codeInvalidParams, Message: "missing required argument: " + name}
		}
	}

	result, err := tool.Handler(ctx, args, boolArg(args, "detail"))
	if err != nil {
		// An unclassified handler error is still a Tool Execution Error,
		// not a protocol error — the tool ran, it just failed
		// (server/tools#error-handling).
		result = ToolResult{Content: err.Error(), IsError: true}
	}
	b, jerr := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": result.Content}},
		"isError": result.IsError,
	})
	if jerr != nil {
		return nil, &rpcError{Code: codeInternalError, Message: jerr.Error()}
	}
	return b, nil
}

func (s *Server) handleResourcesList() (json.RawMessage, *rpcError) {
	resources := make([]map[string]any, 0, len(s.resourceOrder))
	for _, uri := range s.resourceOrder {
		r := s.resources[uri]
		resources = append(resources, map[string]any{
			"uri":         r.URI,
			"name":        r.Name,
			"description": r.Description,
			"mimeType":    r.MimeType,
		})
	}
	b, err := json.Marshal(map[string]any{"resources": resources})
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	return b, nil
}

func (s *Server) handleResourcesRead(ctx context.Context, params json.RawMessage) (json.RawMessage, *rpcError) {
	var req struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &req); err != nil || req.URI == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: a uri is required"}
	}
	res, ok := s.resources[req.URI]
	if !ok {
		// The spec's own chosen code for exactly this
		// (server/resources#error-handling): "Resource not found: -32002".
		return nil, &rpcError{Code: codeResourceNotFound, Message: "Resource not found", Data: map[string]string{"uri": req.URI}}
	}
	text, err := res.Handler(ctx)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	b, jerr := json.Marshal(map[string]any{
		"contents": []map[string]any{{"uri": res.URI, "mimeType": res.MimeType, "text": text}},
	})
	if jerr != nil {
		return nil, &rpcError{Code: codeInternalError, Message: jerr.Error()}
	}
	return b, nil
}

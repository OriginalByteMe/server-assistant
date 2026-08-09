package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// fakeSource is a minimal core.UnraidSource double for these tests.
type fakeSource struct {
	hostInfo      core.HostInfo
	hostErr       error
	array         core.ArrayState
	arrayErr      error
	shares        []core.Share
	sharesErr     error
	containers    []core.Container
	containersErr error
	smart         core.SmartAttrs
	smartErr      error
	reach         core.Reachability
	reachErr      error
}

func (f *fakeSource) HostInfo(context.Context) (core.HostInfo, error) { return f.hostInfo, f.hostErr }
func (f *fakeSource) Array(context.Context) (core.ArrayState, error)  { return f.array, f.arrayErr }
func (f *fakeSource) Shares(context.Context) ([]core.Share, error)    { return f.shares, f.sharesErr }
func (f *fakeSource) Containers(context.Context) ([]core.Container, error) {
	return f.containers, f.containersErr
}
func (f *fakeSource) SmartFor(context.Context, string) (core.SmartAttrs, error) {
	return f.smart, f.smartErr
}
func (f *fakeSource) Reachability(context.Context) (core.Reachability, error) {
	return f.reach, f.reachErr
}

var _ core.UnraidSource = (*fakeSource)(nil)

func newTestServer(src core.UnraidSource) *Server {
	return NewServer(src, NoopProposalSink{}, ServerOptions{})
}

// rpcCall POSTs one JSON-RPC request through the handler and returns the
// decoded response envelope.
func rpcCall(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func TestInitializeHandshake(t *testing.T) {
	s := newTestServer(&fakeSource{})
	resp := rpcCall(t, s, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "0.0.1"},
	})
	require.Nil(t, resp["error"])
	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "expected a result object")
	assert.Equal(t, "2025-11-25", result["protocolVersion"])
	serverInfo, ok := result["serverInfo"].(map[string]any)
	require.True(t, ok)
	assert.NotEmpty(t, serverInfo["name"])
}

func TestPing(t *testing.T) {
	s := newTestServer(&fakeSource{})
	resp := rpcCall(t, s, "ping", nil)
	require.Nil(t, resp["error"])
	_, ok := resp["result"].(map[string]any)
	assert.True(t, ok)
}

func TestToolsListNonEmptyWithCategoriesAndAnnotations(t *testing.T) {
	s := newTestServer(&fakeSource{})
	resp := rpcCall(t, s, "tools/list", nil)
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	tools, ok := result["tools"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, tools)
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name, _ := tool["name"].(string)
		meta, ok := tool["_meta"].(map[string]any)
		require.Truef(t, ok, "tool %q missing _meta", name)
		category, _ := meta["category"].(string)
		assert.NotEmptyf(t, category, "tool %q has no category", name)
		annotations, ok := tool["annotations"].(map[string]any)
		require.Truef(t, ok, "tool %q missing annotations", name)
		for _, key := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
			_, present := annotations[key]
			assert.Truef(t, present, "tool %q annotations missing %q", name, key)
		}
	}
}

func TestToolsCallHappyPath(t *testing.T) {
	src := &fakeSource{hostInfo: core.HostInfo{
		Hostname: "rijkaardserver", UnraidVersion: "7.3.2", CPUPercent: 4.2, UptimeSeconds: 7200,
	}}
	s := newTestServer(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "get_host_info", "arguments": map[string]any{}})
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, false, result["isError"])
	content := result["content"].([]any)
	require.Len(t, content, 1)
	text := content[0].(map[string]any)["text"].(string)
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &v))
	assert.Equal(t, "rijkaardserver", v["hostname"])
	assert.Equal(t, "7.3.2", v["unraidVersion"])
	// summary (no detail:true) must not include the detail-only field.
	_, hasCPUModel := v["cpuModel"]
	assert.False(t, hasCPUModel)
}

func TestToolsCallDetailOptIn(t *testing.T) {
	src := &fakeSource{hostInfo: core.HostInfo{Hostname: "h", CPUModel: "Ryzen 9"}}
	s := newTestServer(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "get_host_info", "arguments": map[string]any{"detail": true}})
	result := resp["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &v))
	assert.Equal(t, "Ryzen 9", v["cpuModel"])
}

// Provenance must reach the LLM, not just internal/web's DTOs. Found live
// on 2026-08-09 via a real Claude MCP session: get_host_info returned a
// CPU percent with no way to tell whether it came from unraid-api, emhttp
// or procfs. It is emitted WITHOUT detail:true — a reading you cannot
// attribute is not usable, so provenance is never a detail-only extra.
func TestSummaryViewsCarryProvenance(t *testing.T) {
	src := &fakeSource{
		hostInfo: core.HostInfo{Hostname: "h", Source: core.SourceProcfs},
		array:    core.ArrayState{State: "STARTED", Source: core.SourceEmhttp},
		shares: []core.Share{
			{Name: "appdata", Source: core.SourceEmhttp},
			{Name: "isos", Source: core.SourceEmhttp},
		},
	}
	s := newTestServer(src)

	for _, tc := range []struct{ tool, want string }{
		{"get_host_info", string(core.SourceProcfs)},
		{"get_array_state", string(core.SourceEmhttp)},
		{"list_shares", string(core.SourceEmhttp)},
	} {
		resp := rpcCall(t, s, "tools/call", map[string]any{"name": tc.tool, "arguments": map[string]any{}})
		require.Nil(t, resp["error"], tc.tool)
		text := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		var v map[string]any
		require.NoError(t, json.Unmarshal([]byte(text), &v), tc.tool)
		assert.Equal(t, tc.want, v["source"], "%s must report where its reading came from", tc.tool)
	}
}

// A shares list whose rows disagree must say "mixed" rather than pick the
// first row's source and pass it off as the whole reading's provenance.
func TestListSharesMixedProvenanceIsNotFlattened(t *testing.T) {
	src := &fakeSource{shares: []core.Share{
		{Name: "appdata", Source: core.SourceUnraidAPI},
		{Name: "isos", Source: core.SourceEmhttp},
	}}
	s := newTestServer(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "list_shares", "arguments": map[string]any{}})
	text := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &v))
	assert.Equal(t, "mixed", v["source"])
}

func TestUnknownMethod(t *testing.T) {
	s := newTestServer(&fakeSource{})
	resp := rpcCall(t, s, "totally/bogus", nil)
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(-32601), errObj["code"])
}

func TestBadParamsUnknownTool(t *testing.T) {
	s := newTestServer(&fakeSource{})
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "does_not_exist"})
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestBadParamsMissingRequiredArgument(t *testing.T) {
	s := newTestServer(&fakeSource{})
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "get_disk_smart", "arguments": map[string]any{}})
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(-32602), errObj["code"])
}

func TestResourceNotFound(t *testing.T) {
	s := newTestServer(&fakeSource{})
	resp := rpcCall(t, s, "resources/read", map[string]any{"uri": "unraid://nonexistent"})
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(-32002), errObj["code"])
}

func TestResourcesListAndRead(t *testing.T) {
	src := &fakeSource{hostInfo: core.HostInfo{Hostname: "h"}}
	s := newTestServer(src)
	listResp := rpcCall(t, s, "resources/list", nil)
	require.Nil(t, listResp["error"])
	resources := listResp["result"].(map[string]any)["resources"].([]any)
	require.NotEmpty(t, resources)

	readResp := rpcCall(t, s, "resources/read", map[string]any{"uri": "unraid://host"})
	require.Nil(t, readResp["error"])
	contents := readResp["result"].(map[string]any)["contents"].([]any)
	require.Len(t, contents, 1)
	text := contents[0].(map[string]any)["text"].(string)
	assert.Contains(t, text, "h")
}

func TestDiskStandbyIsNotAPlainProtocolError(t *testing.T) {
	src := &fakeSource{smartErr: core.ErrDiskStandby}
	s := newTestServer(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "get_disk_smart", "arguments": map[string]any{"device": "/dev/sdd"}})
	require.Nil(t, resp["error"], "disk standby must not be a JSON-RPC protocol error")
	result := resp["result"].(map[string]any)
	assert.Equal(t, true, result["isError"])
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, text, "disk_standby")
}

func TestUnauthenticatedErrorIsStructured(t *testing.T) {
	src := &fakeSource{hostErr: core.ErrUnauthenticated}
	s := newTestServer(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "get_host_info", "arguments": map[string]any{}})
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, true, result["isError"])
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, text, "unauthenticated")
}

func TestGetProposalReportsNotConfigured(t *testing.T) {
	s := newTestServer(&fakeSource{})
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "get_proposal", "arguments": map[string]any{"id": "abc"}})
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, true, result["isError"])
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, text, "not_configured")
}

// wiredSink is a ProposalSink that is fully wired but does not know the id.
// The distinction matters: reporting this as "not_configured" tells the LLM
// to ask a human to finish wiring a seam that is already finished.
type wiredSink struct{}

func (wiredSink) Propose(context.Context, string) (ProposalRef, error) {
	return ProposalRef{}, ErrProposalNotFound
}

func (wiredSink) GetProposal(context.Context, string) (ProposalStatus, error) {
	return ProposalStatus{}, ErrProposalNotFound
}

func TestGetProposalUnknownIDIsNotFound(t *testing.T) {
	s := NewServer(&fakeSource{}, wiredSink{}, ServerOptions{})
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "get_proposal", "arguments": map[string]any{"id": "no-such-id"}})
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	assert.Equal(t, true, result["isError"])
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, text, "not_found")
	assert.NotContains(t, text, "not_configured")
	assert.NotContains(t, text, "HL-SA-18", "a wired sink must not send the LLM off to get the seam wired")
}

func TestGetMethodReturns405(t *testing.T) {
	s := newTestServer(&fakeSource{})
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestTruncationMarksOversizedResult(t *testing.T) {
	containers := make([]core.Container, 0, 300)
	for i := range 300 {
		containers = append(containers, core.Container{
			Name:   fmt.Sprintf("container-with-a-fairly-long-name-%d", i),
			Image:  "example.com/some/long/image/path:latest-tag",
			State:  "running",
			Status: "Up 16 days",
			Ports:  []string{"8080:8080/tcp", "9090:9090/tcp"},
		})
	}
	src := &fakeSource{containers: containers}
	s := newTestServer(src)
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "list_containers", "arguments": map[string]any{"detail": true}})
	require.Nil(t, resp["error"])
	result := resp["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.LessOrEqualf(t, len(text), maxResultBytes+500, "truncated result should stay near the cap, got %d bytes", len(text))
	assert.True(t, strings.Contains(text, "TRUNCATED"), "expected a visible truncation marker, got: %s", text[:200])
}

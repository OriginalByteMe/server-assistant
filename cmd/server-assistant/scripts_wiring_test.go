package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The MCP script tools used to hand-build their JSON with fmt.Sprintf and
// %q. On a []string, %q renders ["a" "b"] — space-separated, no commas —
// which is not JSON. check_script shipped that: it looked correct in every
// manual probe because a rejected script usually has exactly ONE reason,
// and a one-element %q slice happens to be valid JSON by accident. Two
// reasons broke the payload for any strict client.
func TestJSONToolResult_MultipleReasonsStayValidJSON(t *testing.T) {
	res, err := jsonToolResult(map[string]any{
		"valid":    false,
		"reasons":  []string{"(a) script exited nonzero: 1", "(c) boot-write-forbidden"},
		"warnings": []string{},
	})
	require.NoError(t, err)

	var got struct {
		Valid    bool     `json:"valid"`
		Reasons  []string `json:"reasons"`
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content), &got),
		"tool payload must be parseable JSON, got %s", res.Content)
	require.Len(t, got.Reasons, 2)
	require.Equal(t, "(c) boot-write-forbidden", got.Reasons[1])
}

// A nil []string must render as [] ("no reasons"), never null ("reasons
// unknown") — those are different claims and rule 5 forbids conflating them.
func TestJSONToolResult_NilSliceRendersEmptyNotNull(t *testing.T) {
	res, err := jsonToolResult(map[string]any{"reasons": []string(nil)})
	require.NoError(t, err)
	require.JSONEq(t, `{"reasons":[]}`, res.Content)
}

// propose_script was the lone snake_case payload on an otherwise camelCase
// surface, so a client that had learned get_proposal's proposalId found
// nothing to poll with. Keys are asserted here because they are a contract
// with the model, not an implementation detail.
func TestJSONToolResult_ProposeKeysAreCamelCase(t *testing.T) {
	res, err := jsonToolResult(map[string]any{
		"proposalId":   "abc123",
		"dashboardUrl": "http://host:8099/unraid#proposal-abc123",
		"state":        "awaiting_approval",
	})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Content), &got))
	require.Equal(t, "abc123", got["proposalId"])
	require.Equal(t, "awaiting_approval", got["state"])
	require.NotContains(t, got, "proposal_id")
	require.NotContains(t, got, "dashboard_url")
}

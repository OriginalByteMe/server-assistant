package mcp

import (
	"context"
	"errors"

	"server-assistant/internal/core"
)

func registerHostTools(s *Server, source core.UnraidSource) {
	s.Register(Tool{
		Name:        "get_host_info",
		Category:    "host",
		Description: "The Unraid Host itself: hostname, version, CPU and memory usage, uptime.",
		InputSchema: detailOnlySchema("return every host field instead of the summary"),
		Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true},
		Handler: func(ctx context.Context, args map[string]any, detail bool) (ToolResult, error) {
			hi, err := source.HostInfo(ctx)
			if err != nil {
				return handleSourceErr(err)
			}
			return renderResult(hostInfoView(hi, detail))
		},
	})

	s.Register(Tool{
		Name:        "get_reachability",
		Category:    "host",
		Description: "How this process can currently be reached — tailnet-only, publicly served via Funnel, absent, or configured-but-failing (GitHub #51, #56).",
		InputSchema: detailOnlySchema("include the raw tailnet/public URLs instead of just the state"),
		Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: true},
		Handler: func(ctx context.Context, args map[string]any, detail bool) (ToolResult, error) {
			r, err := source.Reachability(ctx)
			if err != nil {
				return handleSourceErr(err)
			}
			return renderResult(reachabilityView(r, detail))
		},
	})
}

// detailOnlySchema is the InputSchema shared by every tool whose only
// parameter is the B2 detail opt-in.
func detailOnlySchema(detailDescription string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"detail": map[string]any{
				"type":        "boolean",
				"description": detailDescription,
			},
		},
		"additionalProperties": false,
	}
}

// renderResult is the common tail of every read-tool handler: marshal the
// view under the B2 byte cap and wrap it as a successful ToolResult.
func renderResult(v any) (ToolResult, error) {
	text, err := render(v)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Content: text}, nil
}

// handleSourceErr classifies the two documented core.UnraidSource sentinel
// errors into structured, LLM-actionable ToolResults (B4); anything else
// is returned as a plain Go error, wrapped by the tools/call dispatcher.
func handleSourceErr(err error) (ToolResult, error) {
	switch {
	case errors.Is(err, core.ErrUnauthenticated):
		return structuredError(
			"unauthenticated", err.Error(),
			"ask a human to create an Unraid API key (a host mutation this server cannot do itself)",
		), nil
	case errors.Is(err, core.ErrDiskStandby):
		return structuredError(
			"disk_standby", "disk is in standby; not woken to read SMART attributes (expected behavior, not a failure)",
			"call again after the disk wakes on its own, or accept the gap",
		), nil
	default:
		return ToolResult{}, err
	}
}

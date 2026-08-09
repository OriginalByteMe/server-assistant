package mcp

import (
	"context"

	"server-assistant/internal/core"
)

func registerStorageTools(s *Server, source core.UnraidSource) {
	s.Register(Tool{
		Name:        "get_array_state",
		Category:    "storage",
		Description: "The array and its parity: state, parity-check progress, and each disk's role, temperature, spin state and coarse SMART status.",
		InputSchema: detailOnlySchema("include per-disk size/used bytes and parity history instead of just the summary"),
		Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true},
		Handler: func(ctx context.Context, args map[string]any, detail bool) (ToolResult, error) {
			a, err := source.Array(ctx)
			if err != nil {
				return handleSourceErr(err)
			}
			return renderResult(arrayView(a, detail))
		},
	})

	s.Register(Tool{
		Name:        "get_disk_smart",
		Category:    "storage",
		Description: "Raw SMART attributes for one disk (read via `smartctl -n standby`, never waking a sleeping disk). Summary returns the handful of attributes that matter for health; detail returns the full table — history across calls is the real signal, not one reading.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"device": map[string]any{
					"type":        "string",
					"description": "device path from get_array_state, e.g. /dev/sdd",
				},
				"detail": map[string]any{
					"type":        "boolean",
					"description": "return every SMART attribute instead of the curated health subset",
				},
			},
			"required":             []string{"device"},
			"additionalProperties": false,
		},
		Required:    []string{"device"},
		Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true},
		Handler: func(ctx context.Context, args map[string]any, detail bool) (ToolResult, error) {
			sm, err := source.SmartFor(ctx, stringArg(args, "device"))
			if err != nil {
				return handleSourceErr(err)
			}
			return renderResult(smartView(sm, detail))
		},
	})

	s.Register(Tool{
		Name:        "list_shares",
		Category:    "storage",
		Description: "User shares: usage, allocator, cache pool, export/accessibility state.",
		InputSchema: detailOnlySchema("include allocator, cache pool and export/accessible flags instead of just name and usage"),
		Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true},
		Handler: func(ctx context.Context, args map[string]any, detail bool) (ToolResult, error) {
			shares, err := source.Shares(ctx)
			if err != nil {
				return handleSourceErr(err)
			}
			return renderResult(map[string]any{
				"shares": sharesView(shares, detail),
				"source": sharesSource(shares),
			})
		},
	})
}

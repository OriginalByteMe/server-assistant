package mcp

import (
	"context"

	"server-assistant/internal/core"
)

func registerContainerTools(s *Server, source core.UnraidSource) {
	s.Register(Tool{
		Name:        "list_containers",
		Category:    "containers",
		Description: "Docker containers on the Host: name, running state, autostart.",
		InputSchema: detailOnlySchema("include image, human status string and published ports instead of just name/state"),
		Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true},
		Handler: func(ctx context.Context, args map[string]any, detail bool) (ToolResult, error) {
			containers, err := source.Containers(ctx)
			if err != nil {
				return handleSourceErr(err)
			}
			return renderResult(map[string]any{"containers": containersView(containers, detail)})
		},
	})
}

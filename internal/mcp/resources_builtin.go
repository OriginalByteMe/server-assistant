package mcp

import (
	"context"

	"server-assistant/internal/core"
)

// registerBuiltinResources wires the four B1 resources: browsable,
// static-ish state a client can list and read without parameters. Each
// reuses the same view builders as the equivalent tool's detail:true
// projection — a resource has no summary/detail knob, it just is what it
// is (specification/2025-11-25/server/resources).
func registerBuiltinResources(s *Server, source core.UnraidSource) {
	s.RegisterResource(Resource{
		URI:         "unraid://host",
		Name:        "Host info",
		Description: "The Unraid Host: hostname, version, CPU/memory, uptime.",
		MimeType:    "application/json",
		Handler: func(ctx context.Context) (string, error) {
			hi, err := source.HostInfo(ctx)
			if err != nil {
				return "", err
			}
			return render(hostInfoView(hi, true))
		},
	})

	s.RegisterResource(Resource{
		URI:         "unraid://array",
		Name:        "Array layout",
		Description: "The array and its parity, and every disk's role, size, temperature and coarse SMART status.",
		MimeType:    "application/json",
		Handler: func(ctx context.Context) (string, error) {
			a, err := source.Array(ctx)
			if err != nil {
				return "", err
			}
			return render(arrayView(a, true))
		},
	})

	s.RegisterResource(Resource{
		URI:         "unraid://shares",
		Name:        "Shares",
		Description: "Every user share: usage, allocator, cache pool, export state.",
		MimeType:    "application/json",
		Handler: func(ctx context.Context) (string, error) {
			shares, err := source.Shares(ctx)
			if err != nil {
				return "", err
			}
			return render(sharesView(shares, true))
		},
	})

	s.RegisterResource(Resource{
		URI:         "unraid://containers",
		Name:        "Containers",
		Description: "Every Docker container on the Host: name, image, state, ports.",
		MimeType:    "application/json",
		Handler: func(ctx context.Context) (string, error) {
			containers, err := source.Containers(ctx)
			if err != nil {
				return "", err
			}
			return render(containersView(containers, true))
		},
	})
}

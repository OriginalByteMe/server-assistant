package main

import (
	"context"

	"server-assistant/internal/commands"
	"server-assistant/internal/web"
)

// commandBridge adapts internal/commands to web.CommandSource.
//
// The two packages define structurally identical Command/CommandResult types
// on purpose: internal/commands must not import internal/web (the dashboard is
// one consumer of the catalog, not its owner), and internal/web must not
// import internal/commands (its seams are narrow and in-package, like
// HarnessSource and ProposalSource). The composition root is the only place
// that knows both, so the translation lives here — the same shape as
// proposalBridge.
type commandBridge struct{ src *commands.Source }

func (b commandBridge) Commands(ctx context.Context) ([]web.Command, error) {
	cs, err := b.src.Commands(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.Command, 0, len(cs))
	for _, c := range cs {
		out = append(out, web.Command{
			ID:          c.ID,
			Label:       c.Label,
			Description: c.Description,
			Consequence: c.Consequence,
		})
	}
	return out, nil
}

// Run passes only the command id through. The catalog re-validates the target
// against the current allowlist before touching Docker, so an id replayed from
// a browser after the operator narrowed the allowlist is refused rather than
// honoured.
func (b commandBridge) Run(ctx context.Context, id, who string) (web.CommandResult, error) {
	r, err := b.src.Run(ctx, id, who)
	return web.CommandResult{
		OK:         r.OK,
		Output:     r.Output,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
	}, err
}

// webCommands converts a possibly-nil catalog into a possibly-nil interface.
// A nil *commands.Source assigned straight into web.CommandSource would yield a
// non-nil interface holding a nil pointer, so the dashboard's "no catalog
// configured" branch would never fire and the run route would register against
// nothing — the same typed-nil trap that crash-looped /api/health earlier.
func webCommands(s *commands.Source) web.CommandSource {
	if s == nil {
		return nil
	}
	return commandBridge{src: s}
}

var _ web.CommandSource = commandBridge{}

package mcp

import "context"

// Resource is one browsable, static-ish piece of Host state (coordinator
// decision B1) — array layout, share list, container list, host info.
// Unlike a Tool it takes no arguments: resources/read is keyed on URI
// alone (specification/2025-11-25/server/resources#reading-resources).
type Resource struct {
	URI         string
	Name        string
	Description string
	MimeType    string
	Handler     ResourceHandler
}

// ResourceHandler renders one resource's current contents.
type ResourceHandler func(ctx context.Context) (text string, err error)

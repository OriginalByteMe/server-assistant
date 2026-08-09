package config

// MCPConfig configures the stateless MCP surface (internal/mcp, HL-SA-17).
// DashboardBaseURL is stitched into the initialize handshake's
// instructions and into get_proposal's not-configured message so the LLM
// can point the human at the right place while the HL-SA-18 grant model
// isn't wired in yet. Empty is a valid, expected default: internal/mcp
// simply omits both mentions rather than guessing a host name.
type MCPConfig struct {
	DashboardBaseURL string `yaml:"dashboard_base_url"`
}

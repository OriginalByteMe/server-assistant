package config

// MCPConfig configures the stateless MCP surface (internal/mcp, HL-SA-17).
// DashboardBaseURL is stitched into the initialize handshake's
// instructions and into get_proposal's not-configured message so the LLM
// can point the human at the right place while the HL-SA-18 grant model
// isn't wired in yet. Empty is a valid, expected default: internal/mcp
// simply omits both mentions rather than guessing a host name.
type MCPConfig struct {
	DashboardBaseURL string `yaml:"dashboard_base_url"`

	// AuthToken gates every request to the MCP endpoint behind
	// `Authorization: Bearer <token>` when set. A secret — resolved from
	// the environment via ${VAR} in Config.resolveSecrets, exactly like
	// unraid.api_key, never a literal in the committed YAML (CONVENTIONS
	// rule 7), never logged (rule 8).
	//
	// This is a shared-secret stand-in for the settled end state (issue
	// #60: unraid-api API keys carrying roles and per-resource
	// permissions), which cannot be built until an API key exists on the
	// host. Exact swap seam: (*mcp.Server).checkAuth in
	// internal/mcp/auth.go — replace its subtle.ConstantTimeCompare call
	// with unraid-api key validation; nothing else in the request path
	// needs to change.
	//
	// Empty (the default) serves the endpoint unauthenticated — Noah's
	// standing decision that development proceeds unauthenticated — and
	// mcp.NewServer logs one startup WARN so that is never silent
	// (CONVENTIONS rule 5: never fail silently into an open endpoint).
	AuthToken string `yaml:"auth_token"`
}

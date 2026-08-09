package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// auth_token is a secret — expanded from the environment via ${VAR} like
// every other secret-bearing field (rule 7), never committed to the YAML
// (HL-SA-17).
func TestLoad_MCPExpandsAuthToken(t *testing.T) {
	t.Setenv("SA_TEST_MCP_TOKEN", "tok-abc123")
	p := writeTemp(t, "schema_version: 1\nmcp:\n  auth_token: \"${SA_TEST_MCP_TOKEN}\"\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-abc123", c.MCP.AuthToken)
}

// A ${VAR} the operator forgot to set is a hard error, never a silent empty
// token (consistent with every other secret resolver).
func TestLoad_MCPRejectsUnsetAuthTokenEnvReference(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\nmcp:\n  auth_token: \"${SA_DEFINITELY_UNSET_MCP_TOKEN}\"\n")
	_, err := NewFileSource(p).Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unset environment variables")
}

// Omitting mcp.auth_token entirely (not a ${VAR} reference at all) is the
// valid default — the MCP endpoint then serves unauthenticated.
func TestLoad_MCPAuthTokenDefaultsEmpty(t *testing.T) {
	p := writeTemp(t, "schema_version: 1\n")
	c, err := NewFileSource(p).Load(context.Background())
	require.NoError(t, err)
	require.Empty(t, c.MCP.AuthToken)
}

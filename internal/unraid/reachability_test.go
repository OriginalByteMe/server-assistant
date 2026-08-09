package unraid

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// fakeTailscale writes an executable shell script that dispatches on its
// first two argv words ("status"/"serve") and prints the matching canned
// JSON. Shapes below are trimmed from real `tailscale status --json` /
// `tailscale serve status --json` captures against rijkaardserver
// (docs/research/mcp-reachability.md §1) plus, for the AllowFunnel case,
// tailscale.com/ipn.ServeConfig's field names (confirmed by reading
// ipn/serve.go in github.com/tailscale/tailscale — that repo has no live
// Funnel-enabled capture to draw from since Funnel was never enabled on the
// reference host during research).
func fakeTailscale(t *testing.T, statusJSON, serveJSON string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tailscale")
	script := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
  "status --json") cat <<'EOF'
%s
EOF
    ;;
  "serve status") cat <<'EOF'
%s
EOF
    ;;
  *) exit 1 ;;
esac
`, statusJSON, serveJSON)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func fakeMissingTailscale(t *testing.T) string {
	return filepath.Join(t.TempDir(), "no-such-tailscale-binary")
}

const runningStatusJSON = `{
  "BackendState": "Running",
  "Self": {
    "Online": true,
    "DNSName": "rijkaardserver.tail8c2c85.ts.net.",
    "TailscaleIPs": ["100.90.134.29"]
  }
}`

func TestReachability_Absent_BinaryMissing(t *testing.T) {
	checker := newReachabilityChecker(fakeMissingTailscale(t), ":8090")
	r, err := checker.Reachability(context.Background())
	require.NoError(t, err, "no tailscale is a valid observation, not a read error")
	assert.Equal(t, core.ReachAbsent, r.State)
}

func TestReachability_Absent_BackendNotRunning(t *testing.T) {
	path := fakeTailscale(t, `{"BackendState":"Stopped","Self":{"Online":false}}`, `{}`)
	checker := newReachabilityChecker(path, ":8090")
	r, err := checker.Reachability(context.Background())
	require.NoError(t, err)
	assert.Equal(t, core.ReachAbsent, r.State)
}

func TestReachability_Tailnet_NoServeMapping(t *testing.T) {
	path := fakeTailscale(t, runningStatusJSON, `{}`)
	checker := newReachabilityChecker(path, ":8090")
	r, err := checker.Reachability(context.Background())
	require.NoError(t, err)
	assert.Equal(t, core.ReachTailnet, r.State)
	assert.Contains(t, r.TailnetURL, "8090")
	assert.Empty(t, r.PublicURL, "PublicURL must only be set in ReachFunnel")
}

func TestReachability_Tailnet_ServeMappingNoFunnel(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	serveJSON := fmt.Sprintf(`{
  "TCP": {"8090": {"HTTPS": true}},
  "Web": {
    "rijkaardserver.tail8c2c85.ts.net:8090": {
      "Handlers": {"/": {"Proxy": %q}}
    }
  }
}`, backend.URL)

	path := fakeTailscale(t, runningStatusJSON, serveJSON)
	checker := newReachabilityChecker(path, ":8090")
	r, err := checker.Reachability(context.Background())
	require.NoError(t, err)
	assert.Equal(t, core.ReachTailnet, r.State, "no AllowFunnel entry means tailnet-only, not funnel")
	assert.Empty(t, r.PublicURL)
}

func TestReachability_Funnel_PublicallyServedAndAnswering(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	const hostport = "rijkaardserver.tail8c2c85.ts.net:8090"
	serveJSON := fmt.Sprintf(`{
  "TCP": {"8090": {"HTTPS": true}},
  "Web": {%q: {"Handlers": {"/": {"Proxy": %q}}}},
  "AllowFunnel": {%q: true}
}`, hostport, backend.URL, hostport)

	path := fakeTailscale(t, runningStatusJSON, serveJSON)
	checker := newReachabilityChecker(path, ":8090")
	r, err := checker.Reachability(context.Background())
	require.NoError(t, err)
	assert.Equal(t, core.ReachFunnel, r.State)
	assert.Contains(t, r.PublicURL, hostport)
	assert.NotEmpty(t, r.TailnetURL, "Funnel state must still carry the tailnet address")
}

func TestReachability_Failing_ConfiguredButBackendDown(t *testing.T) {
	// A serve mapping exists (tailnet-only) but its proxy target is a closed
	// port — the dashboard process itself is down, not Tailscale.
	const hostport = "rijkaardserver.tail8c2c85.ts.net:8090"
	serveJSON := fmt.Sprintf(`{
  "TCP": {"8090": {"HTTPS": true}},
  "Web": {%q: {"Handlers": {"/": {"Proxy": "http://127.0.0.1:1"}}}}
}`, hostport)

	path := fakeTailscale(t, runningStatusJSON, serveJSON)
	checker := newReachabilityChecker(path, ":8090")
	r, err := checker.Reachability(context.Background())
	require.NoError(t, err)
	assert.Equal(t, core.ReachFailing, r.State)
}

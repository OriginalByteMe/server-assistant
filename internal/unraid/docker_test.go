package unraid

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDockerDaemon serves a minimal Docker Engine API over a Unix socket in
// a temp dir (hermetic: no real docker.sock, no network). It returns the
// socket path.
func fakeDockerDaemon(t *testing.T, summaries []dockerContainerSummary, restartPolicies map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "docker.sock")

	mux := http.NewServeMux()
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "true", r.URL.Query().Get("all"), "must list stopped containers too, not just running")
		_ = json.NewEncoder(w).Encode(summaries)
	})
	for _, s := range summaries {
		id := s.ID
		mux.HandleFunc("/containers/"+id+"/json", func(w http.ResponseWriter, r *http.Request) {
			var inspect dockerInspect
			inspect.HostConfig.RestartPolicy.Name = restartPolicies[id]
			_ = json.NewEncoder(w).Encode(inspect)
		})
	}

	l, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	return sockPath
}

func TestDockerClient_Containers(t *testing.T) {
	summaries := []dockerContainerSummary{
		{
			ID:     "abc123",
			Names:  []string{"/plex"},
			Image:  "plexinc/pms-docker",
			State:  "running",
			Status: "Up 16 days",
			Ports: []struct {
				IP          string `json:"IP"`
				PrivatePort int    `json:"PrivatePort"`
				PublicPort  int    `json:"PublicPort"`
				Type        string `json:"Type"`
			}{
				{IP: "0.0.0.0", PrivatePort: 32400, PublicPort: 32400, Type: "tcp"},
			},
		},
		{
			ID:     "def456",
			Names:  []string{"/sonarr"},
			Image:  "linuxserver/sonarr",
			State:  "exited",
			Status: "Exited (0) 3 hours ago",
		},
	}
	restartPolicies := map[string]string{"abc123": "always", "def456": "no"}

	sock := fakeDockerDaemon(t, summaries, restartPolicies)
	client := newDockerClient(sock)

	containers, err := client.containers(context.Background())
	require.NoError(t, err)
	require.Len(t, containers, 2)

	byName := map[string]int{}
	for i, c := range containers {
		byName[c.Name] = i
	}

	plex := containers[byName["plex"]]
	assert.Equal(t, "plexinc/pms-docker", plex.Image)
	assert.Equal(t, "running", plex.State)
	assert.True(t, plex.AutoRun, "restart policy \"always\" must map to AutoRun=true")
	require.Len(t, plex.Ports, 1)
	assert.Contains(t, plex.Ports[0], "32400")

	sonarr := containers[byName["sonarr"]]
	assert.Equal(t, "exited", sonarr.State, "stopped containers must still be listed (all=true)")
	assert.False(t, sonarr.AutoRun, "restart policy \"no\" must map to AutoRun=false")
	assert.Empty(t, sonarr.Ports)
}

func TestDockerClient_SocketUnreachable(t *testing.T) {
	client := newDockerClient(filepath.Join(t.TempDir(), "no-such.sock"))
	_, err := client.containers(context.Background())
	require.Error(t, err, "an unreachable socket must be a read error, never an empty container list")
}

func TestFormatPorts(t *testing.T) {
	ports := []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	}{
		{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
		{PrivatePort: 443, Type: "tcp"}, // no published mapping
	}
	out := formatPorts(ports)
	require.Len(t, out, 2)
	assert.True(t, strings.Contains(out[0], "8080") && strings.Contains(out[0], "80"))
	assert.Equal(t, "443/tcp", out[1])
}

package unraid

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"server-assistant/internal/core"
)

// DockerClient talks to the Docker Engine API over its Unix socket. No
// docker SDK (CONVENTIONS rule 1): stdlib net/http with a custom dialer that
// ignores the request's host and always dials the configured socket path —
// the standard Go pattern for a Unix-socket HTTP client. Exported so
// internal/commands (HL-SA-21's closed operator-command catalog) can reuse
// this exact client for POST /containers/{name}/restart rather than
// standing up a second Unix-socket HTTP implementation.
type DockerClient struct {
	socketPath string
	http       *http.Client
}

func NewDockerClient(socketPath string) *DockerClient {
	dialer := net.Dialer{}
	return &DockerClient{
		socketPath: socketPath,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// dockerContainerSummary is Docker Engine API's `GET /containers/json`
// response item shape (stable, versioned public REST API — the ContainerList
// summary fields have not changed across Engine API versions relevant here).
type dockerContainerSummary struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

// dockerInspect is the subset of `GET /containers/{id}/json` this source
// needs: the summary endpoint above has no restart-policy field at all, so
// AutoRun requires one inspect call per container.
type dockerInspect struct {
	HostConfig struct {
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
}

func (c *DockerClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return fmt.Errorf("unraid docker: build request %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("unraid docker: request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unraid docker: %s returned http %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("unraid docker: decode %s: %w", path, err)
	}
	return nil
}

// Restart issues a Docker Engine API POST /containers/{name}/restart. name
// has already been validated by the caller against its own allowlist (this
// client makes no target-safety decision of its own — CONVENTIONS rule 6)
// and is simply interpolated into the request path.
func (c *DockerClient) Restart(ctx context.Context, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/containers/"+name+"/restart", nil)
	if err != nil {
		return fmt.Errorf("unraid docker: build restart request for %s: %w", name, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("unraid docker: restart %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unraid docker: restart %s returned http %d", name, resp.StatusCode)
	}
	return nil
}

// containers lists every container (running and stopped: "all=true", so a
// crashed/stopped container is diagnosable rather than invisible) and
// inspects each one for its restart policy.
func (c *DockerClient) containers(ctx context.Context) ([]core.Container, error) {
	var summaries []dockerContainerSummary
	if err := c.get(ctx, "/containers/json?all=true", &summaries); err != nil {
		return nil, err
	}

	out := make([]core.Container, 0, len(summaries))
	for _, s := range summaries {
		var inspect dockerInspect
		if err := c.get(ctx, "/containers/"+s.ID+"/json", &inspect); err != nil {
			return nil, fmt.Errorf("unraid docker: inspect %s: %w", strings.TrimPrefix(firstName(s.Names), "/"), err)
		}
		restart := inspect.HostConfig.RestartPolicy.Name
		out = append(out, core.Container{
			Name:    strings.TrimPrefix(firstName(s.Names), "/"),
			Image:   s.Image,
			State:   s.State,
			Status:  s.Status,
			Ports:   formatPorts(s.Ports),
			AutoRun: restart == "always" || restart == "unless-stopped",
		})
	}
	return out, nil
}

func firstName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func formatPorts(ports []struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		if p.PublicPort != 0 {
			out = append(out, fmt.Sprintf("%s:%d->%d/%s", p.IP, p.PublicPort, p.PrivatePort, p.Type))
		} else {
			out = append(out, fmt.Sprintf("%d/%s", p.PrivatePort, p.Type))
		}
	}
	return out
}

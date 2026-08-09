package unraid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"server-assistant/internal/core"
)

// reachabilityChecker implements the dashboard reachability self-check
// (docs/research/mcp-reachability.md §5) by shelling out to the local
// `tailscale` CLI (os/exec, no new Go dependency, matching the research
// doc's own recommendation) rather than linking a Tailscale client library.
type reachabilityChecker struct {
	tailscalePath string
	// dashboardAddr is this process's own HTTP listen address (Config.HTTPAddr,
	// e.g. ":8090"), used to find this dashboard's own port in the live
	// tailscale serve/funnel config rather than a value remembered at wiring
	// time — so the self-check can never report a state that has since
	// changed out-of-band (e.g. a human running `tailscale funnel reset`).
	dashboardAddr string
	probeClient   *http.Client
}

func newReachabilityChecker(tailscalePath, dashboardAddr string) *reachabilityChecker {
	return &reachabilityChecker{
		tailscalePath: tailscalePath,
		dashboardAddr: dashboardAddr,
		probeClient:   &http.Client{},
	}
}

type tailscaleStatus struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		Online       bool     `json:"Online"`
		DNSName      string   `json:"DNSName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
}

// tailscaleServeConfig mirrors tailscale.com/ipn.ServeConfig's JSON shape
// (github.com/tailscale/tailscale, ipn/serve.go — TCP/Web/AllowFunnel have no
// json tag override, so the Go field name is the JSON key; confirmed live
// against the reference host, whose `tailscale serve status --json` returns
// exactly a `{"TCP":{...},"Web":{...}}` object with `AllowFunnel` omitted
// while Funnel is off, matching that struct's `omitempty`).
type tailscaleServeConfig struct {
	TCP map[string]struct {
		HTTPS bool `json:"HTTPS"`
	} `json:"TCP"`
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string `json:"Proxy"`
		} `json:"Handlers"`
	} `json:"Web"`
	AllowFunnel map[string]bool `json:"AllowFunnel"`
}

func runTailscale(ctx context.Context, tailscalePath string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, tailscalePath, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// Reachability implements core.UnraidSource.Reachability. It never returns a
// Go error for "no Tailscale here" — that is state 1 (ReachAbsent), a valid
// observation, not a failed read. An error return means the self-check
// itself could not be completed (e.g. tailscale ran but returned unparsable
// output) — an actual "can't tell" that rule 5 requires surfacing as an
// error, not folding into any of the four states.
func (r *reachabilityChecker) Reachability(ctx context.Context) (core.Reachability, error) {
	now := time.Now()

	statusRaw, err := runTailscale(ctx, r.tailscalePath, "status", "--json")
	if err != nil {
		// Binary missing, tailscaled not running, or no local socket — all
		// collapse to "no Tailscale on the Host at all" per the research
		// doc's state-1 definition; it explicitly short-circuits the rest.
		return core.Reachability{
			State:       core.ReachAbsent,
			Detail:      fmt.Sprintf("tailscale CLI unavailable: %v", err),
			CollectedAt: now,
		}, nil
	}
	var status tailscaleStatus
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		return core.Reachability{}, fmt.Errorf("unraid reachability: decode tailscale status: %w", err)
	}
	if status.BackendState != "Running" || !status.Self.Online {
		return core.Reachability{
			State:       core.ReachAbsent,
			Detail:      fmt.Sprintf("tailscale installed but not running (backend state %q)", status.BackendState),
			CollectedAt: now,
		}, nil
	}

	tailnetHost := strings.TrimSuffix(status.Self.DNSName, ".")
	if tailnetHost == "" && len(status.Self.TailscaleIPs) > 0 {
		tailnetHost = status.Self.TailscaleIPs[0]
	}

	_, portStr, err := net.SplitHostPort(r.dashboardAddr)
	if err != nil {
		// A bare ":port"-less address (or a malformed one) — config.go
		// already guarantees HTTPAddr is non-empty by the time this runs, so
		// treat inability to extract a port as a self-check failure rather
		// than guessing one.
		return core.Reachability{}, fmt.Errorf("unraid reachability: parse dashboard address %q: %w", r.dashboardAddr, err)
	}

	serveRaw, err := runTailscale(ctx, r.tailscalePath, "serve", "status", "--json")
	if err != nil {
		return core.Reachability{}, fmt.Errorf("unraid reachability: run tailscale serve status: %w", err)
	}
	var serve tailscaleServeConfig
	if err := json.Unmarshal(serveRaw, &serve); err != nil {
		return core.Reachability{}, fmt.Errorf("unraid reachability: decode tailscale serve status: %w", err)
	}

	tailnetURL := fmt.Sprintf("http://%s:%s", tailnetHost, portStr)

	// Base tailnet reachability needs no `serve`/`funnel` mapping at all:
	// any port a process on this host listens on is already reachable from
	// other tailnet peers at <tailscale-ip-or-hostname>:<port> — that is how
	// Tailscale's mesh routing works, independent of the hostname/TLS layer
	// `serve`/`funnel` add on top. So find this dashboard's own hostport in
	// the live serve config (if any) only to decide funnel-vs-not and to
	// find a local proxy target worth probing; its absence does not demote
	// the tailnet-reachable state.
	var proxyTarget string
	var funnelOn bool
	var hostport string
	for hp, web := range serve.Web {
		if !strings.HasSuffix(hp, ":"+portStr) {
			continue
		}
		hostport = hp
		if h, ok := web.Handlers["/"]; ok {
			proxyTarget = h.Proxy
		}
		funnelOn = serve.AllowFunnel[hp]
		break
	}

	if hostport == "" {
		return core.Reachability{
			State:       core.ReachTailnet,
			TailnetURL:  tailnetURL,
			Detail:      "reachable on the tailnet; no `tailscale serve`/`funnel` mapping configured for this port yet",
			CollectedAt: now,
		}, nil
	}

	// A mapping exists for this port (tailnet-only or funnel-public): probe
	// the exact local target it proxies to, so a broken dashboard process is
	// reported as failing rather than as whichever of tailnet/funnel the
	// config claims (docs/research/mcp-reachability.md §5, state 4).
	if proxyTarget != "" && !probe(ctx, r.probeClient, proxyTarget) {
		detail := "tailnet-only mapping configured"
		if funnelOn {
			detail = "funnel mapping configured"
		}
		return core.Reachability{
			State:       core.ReachFailing,
			TailnetURL:  tailnetURL,
			Detail:      fmt.Sprintf("%s for %s, but %s does not answer", detail, hostport, proxyTarget),
			CollectedAt: now,
		}, nil
	}

	if funnelOn {
		return core.Reachability{
			State:       core.ReachFunnel,
			PublicURL:   "https://" + hostport,
			TailnetURL:  tailnetURL,
			Detail:      "served publicly via Tailscale Funnel",
			CollectedAt: now,
		}, nil
	}
	return core.Reachability{
		State:       core.ReachTailnet,
		TailnetURL:  tailnetURL,
		Detail:      "reachable on the tailnet only via `tailscale serve` (not Funnel-exposed)",
		CollectedAt: now,
	}, nil
}

// probe issues one short GET against target and reports whether it answered.
// A failure here means the dashboard process itself is unreachable at the
// address Tailscale is configured to forward to — not a Tailscale problem.
func probe(ctx context.Context, client *http.Client, target string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

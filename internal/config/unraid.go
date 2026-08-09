package config

import (
	"fmt"
	"time"
)

// UnraidConfig configures the direct, on-host Unraid state source (HL-SA-22,
// internal/unraid — the concrete core.UnraidSource). Server Assistant runs on
// the Unraid host itself (docs/research/unraid-state-sources.md), so every
// endpoint below is local: unraid-api's GraphQL over nginx, the smartctl
// binary, the Docker Engine socket, and the tailscale CLI for the
// reachability self-check.
//
// A nil *UnraidConfig means no Unraid state source is wired at all — the
// zero-config default. Even wired, GraphQLURL alone is not enough: unraid-api
// rejects every real data field without an APIKey (core.ErrUnauthenticated).
// No API key exists yet on the reference host — creating one is a
// human-approved host mutation (docs/research/unraid-state-sources.md, "Open
// items"), tracked as an open item on HL-SA-22, not something this config
// block can default its way around.
type UnraidConfig struct {
	// GraphQLURL is unraid-api's endpoint. Defaults to the loopback address
	// nginx proxies it on, since the collector runs on the Unraid host.
	GraphQLURL string `yaml:"graphql_url"`
	// APIKey is a secret — supplied via ${VAR}, never the committed YAML
	// (CONVENTIONS rule 7), never logged (rule 8). Sent as the `x-api-key`
	// header per unraid-api's own public docs
	// (github.com/unraid/api/blob/main/api/docs/public/how-to-use-the-api.md)
	// — this exact header was not reproduced against the reference host
	// (creating a key to test it is the same denied host mutation above),
	// so it is corroborated by the vendor's docs, not first-party verified.
	APIKey string `yaml:"api_key"`
	// SmartctlPath is the smartctl binary. Raw SMART attributes are the one
	// gap GraphQL never exposes (unraid-api parses them but discards them
	// before the schema), so this is a direct read, not a GraphQL query.
	SmartctlPath string `yaml:"smartctl_path"`
	// DockerSocket is the Docker Engine API Unix socket path.
	DockerSocket string `yaml:"docker_socket"`
	// TailscalePath is the tailscale CLI used for the reachability
	// self-check (docs/research/mcp-reachability.md §5).
	TailscalePath string `yaml:"tailscale_path"`
	// HostProcPath is the bind-mounted Unraid HOST's procfs (HL-SA-22 host
	// vitals fallback, internal/unraid/procfs.go) — never the container's
	// own /proc, which describes the container and would misreport host
	// CPU/memory/uptime (CONVENTIONS rule 5). docker-compose.yml mounts the
	// real host /proc at exactly this default path
	// (`- /proc:/host/proc:ro`); an absent or unreadable path is a hard
	// error in procfs.go, never a silent fallback to the container's own
	// /proc.
	HostProcPath string `yaml:"host_proc_path"`

	GraphQLTimeoutStr      string `yaml:"graphql_timeout"`
	EmhttpTimeoutStr       string `yaml:"emhttp_timeout"`
	SmartTimeoutStr        string `yaml:"smart_timeout"`
	DockerTimeoutStr       string `yaml:"docker_timeout"`
	ReachabilityTimeoutStr string `yaml:"reachability_timeout"`
	// CPUSampleIntervalStr bounds the gap between the two /proc/stat reads
	// procfs.go's CPU-percent sampler takes: a single read is only
	// cumulative jiffies since boot, not current load, so a delta over a
	// short interval is unavoidable. Short enough to keep the host-info
	// read snappy, long enough for the jiffy counters to move measurably.
	CPUSampleIntervalStr string `yaml:"cpu_sample_interval"`

	graphqlTimeout, emhttpTimeout, smartTimeout, dockerTimeout, reachabilityTimeout, cpuSampleInterval time.Duration // resolved by resolve()
}

// GraphQLTimeout is the per-call deadline enforced on every GraphQL request
// (CONVENTIONS rule 4).
func (u UnraidConfig) GraphQLTimeout() time.Duration { return u.graphqlTimeout }

// EmhttpTimeout bounds one emhttp INI-file read (or small batch of them).
func (u UnraidConfig) EmhttpTimeout() time.Duration { return u.emhttpTimeout }

// SmartTimeout bounds one smartctl invocation. Wider than the others: a
// spinning-up-from-standby disk (which -n standby is specifically avoiding)
// can otherwise take real time to answer even when awake.
func (u UnraidConfig) SmartTimeout() time.Duration { return u.smartTimeout }

// DockerTimeout bounds the container list call plus its per-container
// inspect calls (source.go issues them against one shared deadline).
func (u UnraidConfig) DockerTimeout() time.Duration { return u.dockerTimeout }

// ReachabilityTimeout bounds the tailscale CLI shell-outs and the local
// backend probe in the reachability self-check.
func (u UnraidConfig) ReachabilityTimeout() time.Duration { return u.reachabilityTimeout }

// CPUSampleInterval bounds the gap between procfs.go's two /proc/stat reads
// when computing host CPU percent (see CPUSampleIntervalStr's doc comment).
func (u UnraidConfig) CPUSampleInterval() time.Duration { return u.cpuSampleInterval }

// resolve validates the Unraid block and parses its duration strings,
// applying defaults for every omitted optional knob (rule 6). Mirrors
// SSHConfig.resolve()'s string-field + private-duration + accessor shape
// (rule 3: no library duration-parsing magic outside the Harness exception).
func (u *UnraidConfig) resolve() error {
	if u.GraphQLURL == "" {
		u.GraphQLURL = "http://127.0.0.1/graphql"
	}
	if u.SmartctlPath == "" {
		u.SmartctlPath = "smartctl"
	}
	if u.DockerSocket == "" {
		u.DockerSocket = "/var/run/docker.sock"
	}
	if u.TailscalePath == "" {
		u.TailscalePath = "tailscale"
	}
	if u.HostProcPath == "" {
		u.HostProcPath = "/host/proc"
	}
	var err error
	if u.graphqlTimeout, err = parseDurationDefault(u.GraphQLTimeoutStr, 10*time.Second); err != nil {
		return fmt.Errorf("unraid graphql_timeout: %w", err)
	}
	if u.emhttpTimeout, err = parseDurationDefault(u.EmhttpTimeoutStr, 2*time.Second); err != nil {
		return fmt.Errorf("unraid emhttp_timeout: %w", err)
	}
	if u.smartTimeout, err = parseDurationDefault(u.SmartTimeoutStr, 15*time.Second); err != nil {
		return fmt.Errorf("unraid smart_timeout: %w", err)
	}
	if u.dockerTimeout, err = parseDurationDefault(u.DockerTimeoutStr, 5*time.Second); err != nil {
		return fmt.Errorf("unraid docker_timeout: %w", err)
	}
	if u.reachabilityTimeout, err = parseDurationDefault(u.ReachabilityTimeoutStr, 5*time.Second); err != nil {
		return fmt.Errorf("unraid reachability_timeout: %w", err)
	}
	if u.cpuSampleInterval, err = parseDurationDefault(u.CPUSampleIntervalStr, 250*time.Millisecond); err != nil {
		return fmt.Errorf("unraid cpu_sample_interval: %w", err)
	}
	return nil
}

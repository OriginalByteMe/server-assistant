// Package config defines the typed configuration and Source — the ConfigSource
// seam (CONVENTIONS rule 2). The config file is the single source of truth
// (rule 6); SQLite never holds configuration.
package config

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"server-assistant/internal/core"
)

// SupportedSchemaVersion is the only config schema version this build accepts.
// An absent or mismatched version is a hard error — no silent upgrades.
const SupportedSchemaVersion = 1

// Config is the parsed configuration. The Services list is the source of
// truth (rule 6) — SQLite never holds it. Hosts arrive in a later issue.
type Config struct {
	SchemaVersion int             `yaml:"schema_version"`
	HTTPAddr      string          `yaml:"http_addr"`
	Database      DatabaseConfig  `yaml:"database"`
	Services      []ServiceConfig `yaml:"services"`
	Telegram      TelegramConfig  `yaml:"telegram"`
	// Host is the optional single Unraid box, monitored for reachability. A
	// pointer so "absent" (nil) is distinct from "present and empty": absent
	// means no ADR 0005 gate and the bare spine is wired unchanged.
	Host *HostConfig `yaml:"host"`
	// SSH is the optional shared connection to the Host for container-state
	// and host-metrics probes (ARK-13). Absent ⇒ no SSH probes wired.
	SSH *SSHConfig `yaml:"ssh"`
	// History is the rolling Probe-sample retention window (ARK-9). Not a
	// pointer: always present with a default so storage is bounded even
	// unconfigured (ADR 0002).
	History HistoryConfig `yaml:"history"`
	// Harness is the M2 LLM diagnosis/approval/actuation block (ADR 0009).
	// It ships default-off (ADR 0014): after Load the pointer is always
	// non-nil, and an absent section resolves to Mode off.
	Harness *Harness `yaml:"harness"`
	// Sampler bounds the SMART/capacity/array-state history sampler's
	// interval and retention (GitHub #61). Not a pointer, same as History:
	// always present with a default.
	Sampler SamplerConfig `yaml:"sampler"`
	// Unraid configures the direct on-host Unraid state source (HL-SA-22,
	// internal/unraid). A nil pointer means no Unraid source is wired.
	Unraid *UnraidConfig `yaml:"unraid"`
	// MCP configures the stateless MCP surface (HL-SA-17, internal/mcp).
	// Always present with a default, same as History/Sampler: an empty
	// DashboardBaseURL is valid and simply omits it from tool output.
	MCP MCPConfig `yaml:"mcp"`
	// Scripts bounds the HL-SA-18 script proposal/dry-run/grant subsystem
	// (issue #51/#55). Not a pointer, same as History/Sampler: always
	// present with defaults so grant TTLs are never silently unset.
	Scripts ScriptsConfig `yaml:"scripts"`
}

// HistoryConfig bounds Probe-sample retention. Samples older than Window are
// pruned (ADR 0002). SQLite holds runtime/history only (rule 6); a TSDB
// attaches later behind the same Store seam.
type HistoryConfig struct {
	WindowStr string `yaml:"window"`

	window time.Duration // resolved by validate()
}

// Window is the rolling-retention duration; defaults to 24h.
func (h HistoryConfig) Window() time.Duration { return h.window }

// SSHConfig is the shared, scoped, non-root, read-only Unraid SSH credential
// (CONVENTIONS rule 7 / ADR 0003 hygiene). password is a secret resolved from
// the environment via ${VAR}; key_file is a path to a private key read at
// wiring time. Neither is ever logged (rule 8). One Host ⇒ one SSH block.
type SSHConfig struct {
	Address  string `yaml:"address"` // host:port
	User     string `yaml:"user"`
	Password string `yaml:"password"` // secret: ${VAR}, never committed
	KeyFile  string `yaml:"key_file"` // path to a private key (preferred)
	HostKey  string `yaml:"host_key"` // optional known authorized-key line; empty ⇒ v1 accept-any (ADR 0003)
	Timeout  string `yaml:"timeout"`

	probeTimeout time.Duration // resolved by validate()
}

// ProbeTimeout is the per-SSH-call deadline enforced via context (rule 4).
func (s SSHConfig) ProbeTimeout() time.Duration { return s.probeTimeout }

// HostConfig defines the single Host and its reachability Probe (ADR 0005).
// When set, an unreachable Host turns its Services UNKNOWN (never DOWN) and
// fires exactly one "Host unreachable" Alert. Durations are Go duration
// strings parsed in validate() (no library magic — rule 3).
type HostConfig struct {
	Name         string `yaml:"name"`
	Address      string `yaml:"address"` // host:port reachability target (TCP dial)
	PollInterval string `yaml:"poll_interval"`
	Timeout      string `yaml:"timeout"`
	DebounceN    int    `yaml:"debounce_n"`
	// SSHMetrics drives Host Status from the SSH host-metrics probe
	// (array/disk/parity + CPU/RAM) instead of bare TCP reachability
	// (ARK-13). Requires the shared ssh block.
	SSHMetrics bool `yaml:"ssh_metrics"`

	poll, probeTimeout time.Duration // resolved by validate()
}

// Poll is how often the Host reachability Probe runs.
func (h HostConfig) Poll() time.Duration { return h.poll }

// ProbeTimeout is the per-Probe dial deadline (rule 4).
func (h HostConfig) ProbeTimeout() time.Duration { return h.probeTimeout }

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// TelegramConfig holds the one-way Alert channel's credentials (issue 0003).
// Both fields are secrets — supplied via ${VAR}, never the committed YAML,
// never logged (CONVENTIONS rule 7/8). The whole block is optional: omitted,
// the daemon keeps the Stub notifier (main wiring).
type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
}

// Configured reports whether a usable Telegram channel was supplied. validate()
// has already rejected a half-filled block, so both-empty is the only other
// state: false means "keep the Stub notifier", not "broken config".
func (t TelegramConfig) Configured() bool {
	return t.BotToken != "" && t.ChatID != ""
}

// ServiceConfig defines one monitored HTTP(S) Service. Durations are Go
// duration strings ("30s", "750ms") parsed explicitly in validate() — no
// library magic (rule 3). Consumers read the resolved typed accessors.
type ServiceConfig struct {
	Name             string `yaml:"name"`
	URL              string `yaml:"url"`       // HTTP(S) Service: exactly one of url / tcp / container
	TCPAddr          string `yaml:"tcp"`       // non-HTTP Service: host:port TCP probe
	Container        string `yaml:"container"` // SSH container-state probe (needs the ssh block)
	PollInterval     string `yaml:"poll_interval"`
	Timeout          string `yaml:"timeout"`
	LatencyThreshold string `yaml:"latency_threshold"`
	DebounceN        int    `yaml:"debounce_n"`

	poll, probeTimeout, threshold time.Duration // resolved by validate()
}

// Poll is how often this Service is probed.
func (s ServiceConfig) Poll() time.Duration { return s.poll }

// ProbeTimeout is the per-Probe deadline enforced on this Service (rule 4).
func (s ServiceConfig) ProbeTimeout() time.Duration { return s.probeTimeout }

// Threshold is the latency above which a reachable Service is DEGRADED.
func (s ServiceConfig) Threshold() time.Duration { return s.threshold }

// ReasonerConfig configures the M2 Diagnosis inference backend (ADR 0009).
// api_key is a secret — supply via ${VAR}, expanded in resolveSecrets like
// every other secret-bearing field (rule 7), never logged (rule 8).
type ReasonerConfig struct {
	BaseURL string        `yaml:"base_url"`
	Model   string        `yaml:"model"`
	APIKey  string        `yaml:"api_key"`
	Timeout time.Duration `yaml:"timeout"`
	// Cloud must be true whenever base_url resolves off-box (ADR 0013
	// egress gate): Diagnosis evidence never leaves the host by accident.
	Cloud bool `yaml:"cloud"`
}

// Ceilings bound one Diagnosis cycle (ADR 0009): at most MaxToolCalls
// bounded read-only ReadTool invocations, within WallClock wall-clock time
// total.
type Ceilings struct {
	MaxToolCalls int           `yaml:"max_tool_calls"`
	WallClock    time.Duration `yaml:"wall_clock"`
}

// Harness is the M2 LLM diagnosis/approval/actuation block (ADR 0009). It
// ships default-off (ADR 0014): Config.Harness is always non-nil after Load,
// resolving to Mode "off" when the section is omitted.
//
// Unlike Service/Host/SSH above, the duration fields here decode straight
// from Go duration strings ("30s") into time.Duration via go-yaml's native
// support (confirmed in the pinned github.com/goccy/go-yaml — it already
// calls time.ParseDuration for a time.Duration-typed field), rather than a
// hand-parsed string + private-field + differently-named-accessor pair.
// That's a deliberate, one-time deviation from rule 3's "no library magic"
// for this block only: it is the sole way to expose these fields under
// their exact contracted names (a field and a same-named method cannot
// coexist on one type), which cross-package callers depend on directly.
type Harness struct {
	Mode            string         `yaml:"mode"`
	Reasoner        ReasonerConfig `yaml:"reasoner"`
	Ceilings        Ceilings       `yaml:"ceilings"`
	ApprovalTimeout time.Duration  `yaml:"approval_timeout"`
	Cooldown        time.Duration  `yaml:"cooldown"`
	OutcomeWindow   time.Duration  `yaml:"outcome_window"`
	// Targets maps a configured Service name to its container name (ADR
	// 0018 resolution): the Reasoner only ever names a Service, never a
	// container/host/command.
	Targets map[string]string `yaml:"targets"`
	// AllowRestart narrows the code-owned Action catalog (ADR 0010): config
	// may only shrink what restart_container is allowed to touch, never
	// widen it.
	AllowRestart []string `yaml:"allow_restart"`
	LogLines     int      `yaml:"log_lines"`
	// WriteSSH is the scoped write credential (ADR 0022), distinct from the
	// shared read-only ssh: block. Required in live mode.
	WriteSSH *SSHConfig `yaml:"write_ssh"`
}

// Source is the ConfigSource seam: it yields a validated Config. Hot-reload
// (issue 0008) is a later implementation behind this same seam.
type Source interface {
	Load(ctx context.Context) (*Config, error)
}

// FileSource loads Config from a YAML file on disk.
type FileSource struct {
	path string
}

func NewFileSource(path string) *FileSource {
	return &FileSource{path: path}
}

var _ Source = (*FileSource)(nil)

func (s *FileSource) Load(_ context.Context) (*Config, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", s.path, err)
	}

	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", s.path, err)
	}

	// Expand secrets AFTER parsing so only real values — never comments — are
	// scanned for ${VAR} references (CONVENTIONS rule 7).
	if err := c.resolveSecrets(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", s.path, err)
	}

	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", s.path, err)
	}
	return &c, nil
}

// resolveSecrets replaces ${VAR}/$VAR in operator-supplied string fields with
// environment values so secrets stay out of the committed file. A referenced
// but unset variable is a hard error. Fields are expanded explicitly (no
// reflection — CONVENTIONS rule 3); add new secret-bearing fields here.
func (c *Config) resolveSecrets() error {
	var r secretResolver
	c.HTTPAddr = r.expand(c.HTTPAddr)
	c.Database.Path = r.expand(c.Database.Path)
	for i := range c.Services {
		// A Service URL / TCP target / container name may embed a secret or
		// host-specific value via ${VAR} (rule 7) — expand every probe-kind
		// field so an unset reference is caught at load.
		c.Services[i].URL = r.expand(c.Services[i].URL)
		c.Services[i].TCPAddr = r.expand(c.Services[i].TCPAddr)
		c.Services[i].Container = r.expand(c.Services[i].Container)
	}
	c.Telegram.BotToken = r.expand(c.Telegram.BotToken)
	c.Telegram.ChatID = r.expand(c.Telegram.ChatID)
	// auth_token is a secret — resolved like every other ${VAR} field
	// (rule 7); empty is valid and simply serves the MCP endpoint
	// unauthenticated (HL-SA-17).
	c.MCP.AuthToken = r.expand(c.MCP.AuthToken)
	if c.Unraid != nil {
		c.Unraid.GraphQLURL = r.expand(c.Unraid.GraphQLURL)
		c.Unraid.APIKey = r.expand(c.Unraid.APIKey)
		c.Unraid.SmartctlPath = r.expand(c.Unraid.SmartctlPath)
		c.Unraid.DockerSocket = r.expand(c.Unraid.DockerSocket)
		c.Unraid.TailscalePath = r.expand(c.Unraid.TailscalePath)
	}
	if c.Host != nil {
		// The reachability target may embed a secret host via ${VAR}.
		c.Host.Address = r.expand(c.Host.Address)
	}
	if c.SSH != nil {
		// password is a secret; address/user/key_file may embed ${VAR} too.
		c.SSH.Address = r.expand(c.SSH.Address)
		c.SSH.User = r.expand(c.SSH.User)
		c.SSH.Password = r.expand(c.SSH.Password)
		c.SSH.KeyFile = r.expand(c.SSH.KeyFile)
	}
	if c.Harness != nil {
		// api_key is a secret; write_ssh mirrors the shared ssh: block's
		// secret-bearing fields (rule 7).
		c.Harness.Reasoner.APIKey = r.expand(c.Harness.Reasoner.APIKey)
		if c.Harness.WriteSSH != nil {
			c.Harness.WriteSSH.Address = r.expand(c.Harness.WriteSSH.Address)
			c.Harness.WriteSSH.User = r.expand(c.Harness.WriteSSH.User)
			c.Harness.WriteSSH.Password = r.expand(c.Harness.WriteSSH.Password)
			c.Harness.WriteSSH.KeyFile = r.expand(c.Harness.WriteSSH.KeyFile)
		}
	}
	return r.err()
}

type secretResolver struct {
	missing map[string]struct{}
}

func (r *secretResolver) expand(s string) string {
	return os.Expand(s, func(key string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		if r.missing == nil {
			r.missing = map[string]struct{}{}
		}
		r.missing[key] = struct{}{}
		return ""
	})
}

func (r *secretResolver) err() error {
	if len(r.missing) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.missing))
	for k := range r.missing {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Errorf("config references unset environment variables: %s", strings.Join(keys, ", "))
}

func (c *Config) validate() error {
	if c.SchemaVersion == 0 {
		return errors.New("schema_version is required")
	}
	if c.SchemaVersion != SupportedSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (this build supports %d)", c.SchemaVersion, SupportedSchemaVersion)
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8080"
	}
	if c.Database.Path == "" {
		c.Database.Path = "server-assistant.db"
	}
	if c.SSH != nil {
		if err := c.SSH.resolve(); err != nil {
			return err
		}
	}
	seen := map[string]struct{}{}
	for i := range c.Services {
		if err := c.Services[i].resolve(); err != nil {
			return fmt.Errorf("service %q: %w", c.Services[i].Name, err)
		}
		// A container Service with no ssh block can never be probed — it
		// would be permanently UNKNOWN. Reject at load (rule 6).
		if c.Services[i].Container != "" && c.SSH == nil {
			return fmt.Errorf("service %q: container probe requires an ssh block", c.Services[i].Name)
		}
		if _, dup := seen[c.Services[i].Name]; dup {
			return fmt.Errorf("duplicate service name %q", c.Services[i].Name)
		}
		seen[c.Services[i].Name] = struct{}{}
	}
	// Harness ships default-off (ADR 0014): allocate it here so the pointer
	// is never nil after Load, then resolve its own knobs plus the
	// cross-section checks that only make sense once Services are known.
	if c.Harness == nil {
		c.Harness = &Harness{}
	}
	if err := c.Harness.resolve(seen, c.SSH); err != nil {
		return err
	}
	if c.Host != nil {
		if err := c.Host.resolve(); err != nil {
			return err
		}
		// Host and Services share the dashboard subject namespace (one row
		// per name, one committed-Status key) — a collision would make one
		// silently shadow the other.
		if _, dup := seen[c.Host.Name]; dup {
			return fmt.Errorf("host name %q collides with a service of the same name", c.Host.Name)
		}
		if c.Host.SSHMetrics && c.SSH == nil {
			return errors.New("host ssh_metrics requires an ssh block")
		}
	}
	// A half-filled telegram block is a misconfiguration, not a silent
	// half-on notifier: require both or neither (rule 6).
	if (c.Telegram.BotToken == "") != (c.Telegram.ChatID == "") {
		return errors.New("telegram: bot_token and chat_id must both be set or both omitted")
	}
	if c.History.WindowStr == "" {
		c.History.window = 24 * time.Hour
	} else {
		d, err := time.ParseDuration(c.History.WindowStr)
		if err != nil {
			return fmt.Errorf("history window: %w", err)
		}
		if d < 0 {
			return fmt.Errorf("history window: must not be negative, got %s", c.History.WindowStr)
		}
		// retain <= 0 is the monitor's documented no-prune mode.
		c.History.window = d
	}
	if err := c.Sampler.resolve(); err != nil {
		return err
	}
	if c.Unraid != nil {
		if err := c.Unraid.resolve(); err != nil {
			return err
		}
	}
	if err := c.Scripts.resolve(); err != nil {
		return err
	}
	return nil
}

// resolve validates one Service and parses its duration strings into the
// typed accessors, applying defaults for omitted optional knobs.
func (s *ServiceConfig) resolve() error {
	if s.Name == "" {
		return errors.New("name is required")
	}
	// The name is wired verbatim into the dashboard's HTML element id and the
	// vendored SSE extension's sse-swap event identifier. That extension parses
	// sse-swap as a comma-separated list, so a comma silently splits the
	// subscription and live updates never fire for the Service; newlines/CR
	// break SSE event framing, and quotes/angle-brackets break the HTML
	// attribute. Reject these at load (rule 6: config is the source of truth)
	// rather than ship a half-broken dashboard.
	if err := checkDashboardSafeName(s.Name); err != nil {
		return err
	}
	// A Service is exactly one probe kind — HTTP (url), TCP (tcp), or
	// container-state over SSH (container) — never several, never none: the
	// probe kind must be unambiguous (rule 6). main wires prober.NewHTTP /
	// NewTCP / NewContainerProbe from this.
	kinds := 0
	for _, set := range []bool{s.URL != "", s.TCPAddr != "", s.Container != ""} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return errors.New("exactly one of url, tcp or container is required")
	}
	var err error
	if s.poll, err = parseDurationDefault(s.PollInterval, 30*time.Second); err != nil {
		return fmt.Errorf("poll_interval: %w", err)
	}
	if s.probeTimeout, err = parseDurationDefault(s.Timeout, 10*time.Second); err != nil {
		return fmt.Errorf("timeout: %w", err)
	}
	if s.threshold, err = parseDurationDefault(s.LatencyThreshold, 1*time.Second); err != nil {
		return fmt.Errorf("latency_threshold: %w", err)
	}
	if s.DebounceN == 0 {
		s.DebounceN = 3
	}
	if s.DebounceN < 1 {
		return fmt.Errorf("debounce_n must be >= 1, got %d", s.DebounceN)
	}
	return nil
}

// resolve validates the shared SSH block and parses its timeout, defaulting
// to a tight per-call deadline (rule 4). A credential is mandatory: a probe
// user with neither key nor password can never connect (rule 6). The secret
// itself is never echoed back in any error (rule 8).
func (s *SSHConfig) resolve() error {
	if s.Address == "" {
		return errors.New("ssh: address is required")
	}
	if s.User == "" {
		return errors.New("ssh: user is required")
	}
	if s.Password == "" && s.KeyFile == "" {
		return errors.New("ssh: password or key_file is required")
	}
	var err error
	if s.probeTimeout, err = parseDurationDefault(s.Timeout, 10*time.Second); err != nil {
		return fmt.Errorf("ssh timeout: %w", err)
	}
	return nil
}

// checkDashboardSafeName rejects names carrying characters that break the
// dashboard wiring. The name is wired verbatim into an HTML element id and the
// vendored SSE extension's sse-swap event identifier; that extension parses
// sse-swap as a comma-separated list, so a comma silently splits the
// subscription and live updates never fire; newlines/CR break SSE event
// framing, and quotes/angle-brackets break the HTML attribute. Shared by
// Service and Host — both are first-class dashboard subject rows.
func checkDashboardSafeName(name string) error {
	if strings.ContainsAny(name, ",\n\r\"<>") {
		return fmt.Errorf("name %q: contains characters unsafe for dashboard wiring (one of ,\\n\\r\\\"<>)", name)
	}
	return nil
}

// resolve validates the Host and parses its duration strings, applying
// defaults for omitted optional knobs. Reachability is a quick connectivity
// check, so its timeout default is tighter than a Service's.
func (h *HostConfig) resolve() error {
	if h.Name == "" {
		return errors.New("host: name is required")
	}
	if err := checkDashboardSafeName(h.Name); err != nil {
		return fmt.Errorf("host: %w", err)
	}
	if h.Address == "" {
		return errors.New("host: address is required")
	}
	var err error
	if h.poll, err = parseDurationDefault(h.PollInterval, 30*time.Second); err != nil {
		return fmt.Errorf("host poll_interval: %w", err)
	}
	if h.probeTimeout, err = parseDurationDefault(h.Timeout, 5*time.Second); err != nil {
		return fmt.Errorf("host timeout: %w", err)
	}
	if h.DebounceN == 0 {
		h.DebounceN = 3
	}
	if h.DebounceN < 1 {
		return fmt.Errorf("host debounce_n must be >= 1, got %d", h.DebounceN)
	}
	return nil
}

// containerNameRe matches the safe character set for a Docker container name
// and for the Service key naming it in targets/allow_restart. The Actuator
// interpolates these into an SSH restart command, so anything outside
// [A-Za-z0-9_.-] is rejected at load (rule 6).
var containerNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// isLoopbackHost reports whether host is one of the ADR 0013 loopback
// spellings. No net.ParseIP: the ADR names an exact literal set, not "any IP
// that happens to be loopback".
func isLoopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}

// resolve validates the Harness block and applies defaults for every
// omitted optional knob (rule 6). serviceNames is the set of configured
// Service names — a targets key must name one of them (ADR 0018); readSSH
// is the shared read-only ssh: block, compared against write_ssh under
// ADR 0022.
func (h *Harness) resolve(serviceNames map[string]struct{}, readSSH *SSHConfig) error {
	mode, err := core.ParseHarnessMode(h.Mode)
	if err != nil {
		return fmt.Errorf("harness: %w", err)
	}

	if h.Reasoner.Timeout == 0 {
		h.Reasoner.Timeout = 60 * time.Second
	}
	if h.Ceilings.MaxToolCalls == 0 {
		h.Ceilings.MaxToolCalls = 4
	}
	if h.Ceilings.WallClock == 0 {
		h.Ceilings.WallClock = 120 * time.Second
	}
	if h.ApprovalTimeout == 0 {
		h.ApprovalTimeout = 10 * time.Minute
	}
	if h.Cooldown == 0 {
		h.Cooldown = 15 * time.Minute
	}
	if h.OutcomeWindow == 0 {
		h.OutcomeWindow = 3 * time.Minute
	}
	if h.LogLines == 0 {
		h.LogLines = 50
	}

	// Ceilings, timeouts, and target naming are checked regardless of mode:
	// an off/shadow block that is already broken should not pass silently
	// and only blow up the moment an operator flips mode: live.
	if h.Ceilings.MaxToolCalls < 1 || h.Ceilings.MaxToolCalls > 20 {
		return fmt.Errorf("harness: ceilings.max_tool_calls must be 1..20, got %d", h.Ceilings.MaxToolCalls)
	}
	if h.LogLines < 1 || h.LogLines > 200 {
		return fmt.Errorf("harness: log_lines must be 1..200, got %d", h.LogLines)
	}
	if h.Ceilings.WallClock <= 0 {
		return fmt.Errorf("harness: ceilings.wall_clock must be positive, got %s", h.Ceilings.WallClock)
	}
	if h.ApprovalTimeout <= 0 {
		return fmt.Errorf("harness: approval_timeout must be positive, got %s", h.ApprovalTimeout)
	}
	if h.Cooldown <= 0 {
		return fmt.Errorf("harness: cooldown must be positive, got %s", h.Cooldown)
	}
	if h.OutcomeWindow <= 0 {
		return fmt.Errorf("harness: outcome_window must be positive, got %s", h.OutcomeWindow)
	}
	if h.Reasoner.Timeout <= 0 {
		return fmt.Errorf("harness: reasoner.timeout must be positive, got %s", h.Reasoner.Timeout)
	}

	for svc, container := range h.Targets {
		if _, ok := serviceNames[svc]; !ok {
			return fmt.Errorf("harness: targets key %q does not name a configured service", svc)
		}
		if !containerNameRe.MatchString(container) {
			return fmt.Errorf("harness: targets[%q] value %q has invalid characters", svc, container)
		}
	}
	for _, name := range h.AllowRestart {
		if !containerNameRe.MatchString(name) {
			return fmt.Errorf("harness: allow_restart entry %q has invalid characters", name)
		}
	}

	if mode == core.HarnessOff {
		return nil
	}

	if h.Reasoner.BaseURL == "" {
		return errors.New("harness: reasoner.base_url is required when mode is not off")
	}
	if h.Reasoner.Model == "" {
		return errors.New("harness: reasoner.model is required when mode is not off")
	}
	u, err := url.Parse(h.Reasoner.BaseURL)
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("harness: reasoner.base_url must be an absolute http(s) URL, got %q", h.Reasoner.BaseURL)
	}
	loopback := isLoopbackHost(u.Hostname())
	if loopback && h.Reasoner.Cloud {
		return errors.New("harness: reasoner.cloud must be false for a loopback base_url (ADR 0013 egress gate)")
	}
	if !loopback && !h.Reasoner.Cloud {
		return errors.New("harness: reasoner.base_url resolves off-box, which requires cloud: true (ADR 0013 egress gate)")
	}

	if mode != core.HarnessLive {
		return nil
	}

	allowed := make(map[string]struct{}, len(h.AllowRestart))
	for _, name := range h.AllowRestart {
		allowed[name] = struct{}{}
	}
	for svc, container := range h.Targets {
		if _, ok := allowed[container]; !ok {
			return fmt.Errorf("harness: live mode target %q (%s) is not in allow_restart (ADR 0010: config narrows, never widens)", svc, container)
		}
	}

	if h.WriteSSH == nil {
		return errors.New("harness: write_ssh is required in live mode")
	}
	if err := h.WriteSSH.resolve(); err != nil {
		return fmt.Errorf("harness: write_ssh: %w", err)
	}
	if readSSH != nil && h.WriteSSH.User == readSSH.User &&
		h.WriteSSH.KeyFile == readSSH.KeyFile && h.WriteSSH.Password == readSSH.Password {
		return errors.New("harness: write_ssh must be a distinct credential from ssh: (ADR 0022 — the write path never reuses the read-only probe credential)")
	}
	return nil
}

func parseDurationDefault(v string, def time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive, got %s", v)
	}
	return d, nil
}

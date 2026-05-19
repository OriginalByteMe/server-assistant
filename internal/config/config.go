// Package config defines the typed configuration and Source — the ConfigSource
// seam (CONVENTIONS rule 2). The config file is the single source of truth
// (rule 6); SQLite never holds configuration.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
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
	var herr error
	if c.History.window, herr = parseDurationDefault(c.History.WindowStr, 24*time.Hour); herr != nil {
		return fmt.Errorf("history window: %w", herr)
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

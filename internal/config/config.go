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

	"github.com/goccy/go-yaml"
)

// SupportedSchemaVersion is the only config schema version this build accepts.
// An absent or mismatched version is a hard error — no silent upgrades.
const SupportedSchemaVersion = 1

// Config is the parsed configuration. Services/Hosts are added in later issues.
type Config struct {
	SchemaVersion int            `yaml:"schema_version"`
	HTTPAddr      string         `yaml:"http_addr"`
	Database      DatabaseConfig `yaml:"database"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
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
	return nil
}

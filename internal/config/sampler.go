package config

import (
	"fmt"
	"time"
)

// SamplerConfig bounds the SMART/capacity sampler's poll interval and how
// long its history is retained (GitHub #61). Not a pointer: always present
// with a default so history is bounded even unconfigured, same as
// HistoryConfig (rule 6).
//
// Interval default: 15 minutes. SMART attributes, disk/share capacity and
// array state all move on the order of hours to days, not seconds — a
// tighter interval buys no earlier signal on a slow reallocated-sector
// creep and only adds needless smartctl invocations against awake disks
// (GitHub #61's governing constraint: spin-up risk, not resolution).
//
// Retention default: 90 days. Long enough to compare "this week" against a
// season-old baseline — the whole reason this history exists — short
// enough that ~35k rows per series (4 samples/hour * 24h * 90d) never
// becomes a storage concern worth downsampling for.
type SamplerConfig struct {
	IntervalStr  string `yaml:"interval"`
	RetentionStr string `yaml:"retention"`

	interval  time.Duration // resolved by validate()
	retention time.Duration // resolved by validate()
}

// Interval is how often the sampler reads SMART/capacity/array state.
func (s SamplerConfig) Interval() time.Duration { return s.interval }

// Retention is how long sampled history is kept before pruning.
func (s SamplerConfig) Retention() time.Duration { return s.retention }

// resolve validates the Sampler block and parses its duration strings,
// applying the defaults documented on SamplerConfig for omitted knobs
// (rule 6), matching the hand-parsed-duration convention the rest of this
// package uses (rule 3) rather than go-yaml's native time.Duration support.
func (s *SamplerConfig) resolve() error {
	interval, err := parseDurationDefault(s.IntervalStr, 15*time.Minute)
	if err != nil {
		return fmt.Errorf("sampler interval: %w", err)
	}
	s.interval = interval

	retention, err := parseDurationDefault(s.RetentionStr, 90*24*time.Hour)
	if err != nil {
		return fmt.Errorf("sampler retention: %w", err)
	}
	s.retention = retention
	return nil
}

// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads and validates the postfix-report YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from strings like "90s" or "1h".
type Duration struct{ time.Duration }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, s)
	}
	d.Duration = v
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// Config is the effective configuration. See config.example.yaml for the
// documented schema.
type Config struct {
	Database struct {
		Path string `yaml:"path"`
	} `yaml:"database"`
	Input struct {
		Paths    []string `yaml:"paths"`
		Timezone string   `yaml:"timezone"`
		// Year forces the year of BSD syslog timestamps (0 = infer from mtime).
		Year int `yaml:"year"`
		// Hosts restricts import to these syslog hosts (empty = all).
		Hosts []string `yaml:"hosts"`
	} `yaml:"input"`
	Correlation struct {
		FilterTransports []string `yaml:"filter_transports"`
		ReinjectUIDs     []int    `yaml:"reinject_uids"`
		ReinjectWindow   Duration `yaml:"reinject_window"`
		StaleAfter       Duration `yaml:"stale_after"`
	} `yaml:"correlation"`
	Coverage struct {
		GapThreshold Duration `yaml:"gap_threshold"`
	} `yaml:"coverage"`
	Output struct {
		Directory string `yaml:"directory"`
		Title     string `yaml:"title"`
	} `yaml:"output"`
	Reports struct {
		Daily    bool `yaml:"daily"`
		Weekly   bool `yaml:"weekly"`
		Monthly  bool `yaml:"monthly"`
		Yearly   bool `yaml:"yearly"`
		TopLimit int  `yaml:"top_limit"`
	} `yaml:"reports"`
	Privacy struct {
		AnonymizeEmail bool `yaml:"anonymize_email"`
		AnonymizeIP    bool `yaml:"anonymize_ip"`
	} `yaml:"privacy"`
	Logging struct {
		Level string `yaml:"level"`
	} `yaml:"logging"`

	// Location is the parsed Input.Timezone (not part of the YAML).
	Location *time.Location `yaml:"-"`
}

// Default returns a configuration with all defaults applied.
func Default() *Config {
	c := &Config{}
	c.Database.Path = "/var/lib/postfix-report/report.db"
	c.Input.Paths = []string{"/var/log/maillog", "/var/log/maillog-*"}
	c.Input.Timezone = "Europe/Rome"
	c.Correlation.FilterTransports = []string{"filter"}
	c.Correlation.ReinjectUIDs = []int{97}
	c.Correlation.ReinjectWindow = Duration{120 * time.Second}
	c.Correlation.StaleAfter = Duration{7 * 24 * time.Hour}
	c.Coverage.GapThreshold = Duration{60 * time.Minute}
	c.Output.Directory = "/var/www/html/postfix-report"
	c.Output.Title = "Postfix report"
	c.Reports.Daily, c.Reports.Weekly, c.Reports.Monthly, c.Reports.Yearly = true, true, true, true
	c.Reports.TopLimit = 20
	c.Logging.Level = "info"
	return c
}

// Load reads a YAML file over the defaults. Unknown keys are errors.
func Load(path string) (*Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Finish(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Finish validates c and resolves derived values. Load calls it; callers
// that build a Config programmatically (tests) must call it too.
func (c *Config) Finish() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	if c.Database.Path == "" {
		add("database.path: required")
	}
	loc, err := time.LoadLocation(c.Input.Timezone)
	if err != nil {
		add("input.timezone: %v", err)
	}
	c.Location = loc
	if c.Input.Year != 0 && (c.Input.Year < 1990 || c.Input.Year > 2200) {
		add("input.year: %d out of range", c.Input.Year)
	}
	for _, p := range c.Input.Paths {
		if _, err := filepath.Match(p, ""); err != nil {
			add("input.paths: invalid pattern %q", p)
		}
	}
	if c.Correlation.ReinjectWindow.Duration <= 0 {
		add("correlation.reinject_window: must be > 0")
	}
	if c.Correlation.StaleAfter.Duration < time.Hour {
		add("correlation.stale_after: must be >= 1h")
	}
	for _, u := range c.Correlation.ReinjectUIDs {
		if u < 0 {
			add("correlation.reinject_uids: negative uid %d", u)
		}
	}
	if c.Coverage.GapThreshold.Duration < time.Minute {
		add("coverage.gap_threshold: must be >= 1m")
	}
	if c.Output.Directory == "" {
		add("output.directory: required")
	}
	if c.Reports.TopLimit < 1 || c.Reports.TopLimit > 500 {
		add("reports.top_limit: must be between 1 and 500")
	}
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		add("logging.level: must be debug, info, warn or error")
	}
	if c.Output.Directory != "" && c.Database.Path != "" {
		out, _ := filepath.Abs(c.Output.Directory)
		db, _ := filepath.Abs(c.Database.Path)
		if rel, err := filepath.Rel(out, db); err == nil && !strings.HasPrefix(rel, "..") {
			add("database.path must not be inside output.directory (the database would be published)")
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// IsFilterTransport reports whether relay names a content-filter transport.
func (c *Config) IsFilterTransport(relay string) bool {
	for _, t := range c.Correlation.FilterTransports {
		if relay == t {
			return true
		}
	}
	return false
}

// IsReinjectUID reports whether uid is used by the content filter to reinject mail.
func (c *Config) IsReinjectUID(uid int) bool {
	for _, u := range c.Correlation.ReinjectUIDs {
		if uid == u {
			return true
		}
	}
	return false
}

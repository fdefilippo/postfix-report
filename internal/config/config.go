// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads and validates the postfix-report YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
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
		Language  string `yaml:"language"` // "it" (default) or "en"
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
	Web struct {
		Listen       string   `yaml:"listen"`
		Prefix       string   `yaml:"prefix"`
		PublicOrigin string   `yaml:"public_origin"`
		Workers      int      `yaml:"workers"`
		QueueSize    int      `yaml:"queue_size"`
		JobTimeout   Duration `yaml:"job_timeout"`
		JobTTL       Duration `yaml:"job_ttl"`
	} `yaml:"web"`

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
	c.Output.Language = "it"
	c.Web.Listen = "127.0.0.1:8080"
	c.Web.Prefix = "/"
	c.Web.Workers = 1
	c.Web.QueueSize = 4
	c.Web.JobTimeout = Duration{2 * time.Minute}
	c.Web.JobTTL = Duration{10 * time.Minute}
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
	switch c.Output.Language {
	case "it", "en":
	default:
		add("output.language: must be it or en")
	}
	if c.Reports.TopLimit < 1 || c.Reports.TopLimit > 500 {
		add("reports.top_limit: must be between 1 and 500")
	}
	if host, _, err := net.SplitHostPort(c.Web.Listen); err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		add("web.listen: must be a numeric loopback address and port")
	}
	if !strings.HasPrefix(c.Web.Prefix, "/") || !strings.HasSuffix(c.Web.Prefix, "/") || strings.Contains(c.Web.Prefix, "..") || strings.ContainsAny(c.Web.Prefix, "\\?#%") || strings.Contains(c.Web.Prefix, "//") {
		add("web.prefix: must be an absolute URL path ending in / without traversal")
	}
	for _, r := range c.Web.Prefix {
		if !(r == '/' || r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			add("web.prefix: unsupported URL path character")
			break
		}
	}
	if _, port, err := net.SplitHostPort(c.Web.Listen); err == nil {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			add("web.listen: port must be between 1 and 65535")
		}
	}
	if c.Web.PublicOrigin != "" {
		u, err := url.Parse(c.Web.PublicOrigin)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			add("web.public_origin: expected http(s)://host[:port] without path")
		}
	}
	if c.Web.Workers < 1 || c.Web.Workers > 4 {
		add("web.workers: must be between 1 and 4")
	}
	if c.Web.QueueSize < 0 || c.Web.QueueSize > 32 {
		add("web.queue_size: must be between 0 and 32")
	}
	if c.Web.JobTimeout.Duration < 10*time.Second || c.Web.JobTimeout.Duration > 10*time.Minute {
		add("web.job_timeout: must be between 10s and 10m")
	}
	if c.Web.JobTTL.Duration < time.Minute || c.Web.JobTTL.Duration > time.Hour {
		add("web.job_ttl: must be between 1m and 1h")
	}
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		add("logging.level: must be debug, info, warn or error")
	}
	if c.Output.Directory != "" && c.Database.Path != "" {
		out, outErr := resolvedPath(c.Output.Directory)
		db, dbErr := resolvedPath(c.Database.Path)
		if outErr != nil {
			add("output.directory: %v", outErr)
		}
		if dbErr != nil {
			add("database.path: %v", dbErr)
		}
		if outErr == nil && dbErr == nil {
			// Keep the lexical guard too: a public symlink can expose a DB
			// whose ultimate target is outside the webroot. Check its resolved
			// directory entry before following the final filename symlink.
			rawOut, err := filepath.Abs(c.Output.Directory)
			if err != nil {
				add("output.directory: %v", err)
			}
			rawDB, err := filepath.Abs(c.Database.Path)
			if err != nil {
				add("database.path: %v", err)
			}
			parent, leaf := filepath.Split(c.Database.Path)
			if parent == "" {
				parent = "."
			}
			entryParent, err := resolvedPath(parent)
			if err != nil {
				add("database.path: %v", err)
			}
			inside := func(root, path string) bool {
				rel, err := filepath.Rel(root, path)
				return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
			}
			if inside(rawOut, rawDB) || inside(out, rawDB) || inside(out, db) || inside(out, filepath.Join(entryParent, leaf)) {
				add("database.path must not be inside output.directory (the database would be published)")
			}
			// Use the checked paths consistently: filepath.Join would otherwise
			// clean symlink/.. lexically while SQLite follows filesystem order.
			c.Output.Directory, c.Database.Path = out, db
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

// resolvedPath resolves components in filesystem order, retaining missing
// suffixes. In particular, symlink/.. must traverse the link before ..;
// cleaning the path lexically first can hide a database inside the webroot.
func resolvedPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = wd + string(filepath.Separator) + path
	}
	volume := filepath.VolumeName(path)
	base := volume + string(filepath.Separator)
	parts := strings.Split(path[len(volume):], string(filepath.Separator))
	for i, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			base = filepath.Dir(base)
			continue
		}
		next := filepath.Join(base, part)
		_, err := os.Lstat(next)
		if err != nil {
			if !os.IsNotExist(err) {
				return "", err
			}
			base = next
			continue
		}
		base, err = filepath.EvalSymlinks(next)
		if err != nil {
			return "", err
		}
		if i < len(parts)-1 {
			st, err := os.Stat(base)
			if err != nil {
				return "", err
			}
			if !st.IsDir() {
				return "", fmt.Errorf("path component %q is not a directory", next)
			}
		}
	}
	return base, nil
}

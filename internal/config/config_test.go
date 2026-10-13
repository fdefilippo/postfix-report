// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaultsAndOverrides(t *testing.T) {
	c, err := Load(write(t, "database:\n  path: /x/r.db\ninput:\n  timezone: UTC\n  paths: [/var/log/maillog*]\ncorrelation:\n  reinject_window: 30s\nreports:\n  top_limit: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Database.Path != "/x/r.db" || c.Location.String() != "UTC" || c.Correlation.ReinjectWindow.Seconds() != 30 ||
		c.Reports.TopLimit != 5 || !c.IsReinjectUID(97) || !c.IsFilterTransport("filter") {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestEmptyFileUsesDefaults(t *testing.T) {
	if _, err := Load(write(t, "")); err != nil {
		t.Fatal(err)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"unknown key":   "databse:\n  path: x\n",
		"bad tz":        "input:\n  timezone: Mars/Base\n",
		"bad duration":  "coverage:\n  gap_threshold: soon\n",
		"top limit":     "reports:\n  top_limit: 0\n",
		"db in webroot": "database:\n  path: /srv/www/r.db\noutput:\n  directory: /srv/www\n",
		"log level":     "logging:\n  level: chatty\n",
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: expected error", name)
		} else if testing.Verbose() {
			t.Logf("%s: %v", name, err)
		}
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	p := filepath.Join("..", "..", "config.example.yaml")
	if _, err := os.Stat(p); err != nil {
		t.Skip("config.example.yaml not present")
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(c.Database.Path, ".db") {
		t.Errorf("db path %q", c.Database.Path)
	}
}

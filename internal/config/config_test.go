// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestWebConfigurationBoundaries(t *testing.T) {
	for _, entry := range []string{"listen: 0.0.0.0:8080", "listen: localhost:8080", "prefix: /../", "prefix: /bad\"/", "public_origin: https://example.org/path", "workers: 100", "queue_size: -1", "job_timeout: 1s", "job_ttl: 10h"} {
		if _, err := Load(write(t, "web:\n  "+entry+"\n")); err == nil {
			t.Error("accepted", entry)
		}
	}
	c, err := Load(write(t, "web:\n  listen: '[::1]:8080'\n  prefix: /postfix-report/\n  public_origin: https://reports.example.org\n  job_timeout: 30s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Web.Workers != 1 || c.Web.QueueSize != 4 || c.Web.Prefix != "/postfix-report/" {
		t.Fatal("web defaults lost")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"unknown key":   "databse:\n  path: x\n",
		"bad tz":        "input:\n  timezone: Mars/Base\n",
		"bad duration":  "coverage:\n  gap_threshold: soon\n",
		"top limit":     "reports:\n  top_limit: 0\n",
		"language":      "output:\n  language: fr\n",
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

func TestCanonicalDatabaseBoundary(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(web, "nested")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	viaNested := filepath.Join(root, "via-nested")
	if err := os.Symlink(nested, viaNested); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(web, alias); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, out, db string
		bad           bool
	}{
		{"database alias", web, filepath.Join(alias, "new", "r.db"), true},
		{"symlink then dotdot", web, viaNested + "/../r.db", true},
		{"webroot symlink then dotdot", viaNested + "/..", filepath.Join(web, "r.db"), true},
		{"webroot alias", alias, filepath.Join(web, "r.db"), true},
		{"dot prefix", web, filepath.Join(web, "..private", "r.db"), true},
		{"missing parents", filepath.Join(web, "new"), filepath.Join(alias, "new", "nested", "r.db"), true},
		{"sibling", web, filepath.Join(outside, "r.db"), false},
		{"legitimate output alias", alias, filepath.Join(outside, "r.db"), false},
		{"similar prefix", web, web + "-private/r.db", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Output.Directory = tc.out
			c.Database.Path = tc.db
			err := c.Finish()
			if (err != nil) != tc.bad {
				t.Fatalf("Finish()=%v, want bad=%t", err, tc.bad)
			}
		})
	}
	outsideFile := filepath.Join(outside, "existing.db")
	if err := os.WriteFile(outsideFile, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	publicLink := filepath.Join(web, "public.db")
	if err := os.Symlink(outsideFile, publicLink); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{web, alias} {
		c := Default()
		c.Output.Directory = out
		c.Database.Path = publicLink
		if err := c.Finish(); err == nil {
			t.Fatal("public symlink to private DB accepted")
		}
	}

	for _, tc := range []struct{ name, target string }{{"dangling", filepath.Join(root, "absent")}, {"loop", filepath.Join(root, "loop")}} {
		link := filepath.Join(root, tc.name)
		if err := os.Symlink(tc.target, link); err != nil {
			t.Fatal(err)
		}
		c := Default()
		c.Output.Directory = web
		c.Database.Path = filepath.Join(link, "r.db")
		if err := c.Finish(); err == nil {
			t.Errorf("%s: accepted unresolved symlink", tc.name)
		}
	}
}

func TestYAMLCompatibility(t *testing.T) {
	body := "input:\n  paths: []\n  hosts: []\ncorrelation:\n  filter_transports: []\n  reinject_uids: []\n  reinject_window: 45s\nreports:\n  daily: no\nprivacy:\n  anonymize_ip: on\n"
	c, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Input.Paths) != 0 || len(c.Input.Hosts) != 0 || len(c.Correlation.FilterTransports) != 0 || len(c.Correlation.ReinjectUIDs) != 0 || c.Reports.Daily || !c.Reports.Weekly || !c.Privacy.AnonymizeIP || c.Correlation.ReinjectWindow.Duration != 45*time.Second {
		t.Fatalf("changed YAML semantics: %+v", c)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	round, err := Load(write(t, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if round.Correlation.ReinjectWindow != c.Correlation.ReinjectWindow || round.Reports != c.Reports || round.Privacy != c.Privacy || len(round.Input.Paths) != 0 {
		t.Fatal("YAML roundtrip changed config")
	}
	if _, err := Load(write(t, "reports:\n  daily: true\n  daily: false\n")); err == nil {
		t.Fatal("duplicate key accepted")
	}
}

func TestNotificationWindowValidation(t *testing.T) {
	for _, value := range []string{"0s", "-1s", "1500ms", "169h"} {
		if _, err := Load(write(t, "correlation:\n  notification_window: "+value+"\n")); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
	c, err := Load(write(t, "correlation:\n  notification_window: 120s\n"))
	if err != nil || c.Correlation.NotificationWindow.Seconds() != 120 {
		t.Fatalf("window: %+v %v", c, err)
	}
}

// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

// e2e prepares a config file pointing at a synthetic, rotated log set.
func e2e(t *testing.T) (cfgPath, dir string) {
	dir = t.TempDir()
	logs := filepath.Join(dir, "log")
	loc, _ := time.LoadLocation("Europe/Rome")
	var buf bytes.Buffer
	if err := testutil.Synthetic(&buf, testutil.SynthConfig{Start: time.Date(2026, 9, 25, 0, 0, 0, 0, loc), Days: 10, PerDay: 30, GapDay: -1, Seed: 3}); err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(buf.String(), "\n")
	half := len(lines) / 2
	mt := time.Date(2026, 10, 5, 12, 0, 0, 0, loc)
	testutil.WriteFile(t, filepath.Join(logs, "maillog-20261001.gz"), strings.Join(lines[:half], ""), mt)
	testutil.WriteFile(t, filepath.Join(logs, "maillog"), strings.Join(lines[half:], ""), mt)
	cfgPath = filepath.Join(dir, "config.yaml")
	cfg := fmt.Sprintf(`database:
  path: %s/var/report.db
input:
  paths:
    - %s/maillog
    - %s/maillog-*.gz
  timezone: Europe/Rome
output:
  directory: %s/www
logging:
  level: warn
`, dir, logs, logs, dir)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, dir
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCLIEndToEnd(t *testing.T) {
	cfg, dir := e2e(t)
	if code, _, e := runCLI(t, "-config", cfg, "check-config"); code != exitOK {
		t.Fatalf("check-config: %d %s", code, e)
	}
	code, out, e := runCLI(t, "-config", cfg, "update")
	if code != exitOK {
		t.Fatalf("update: %d\n%s\n%s", code, out, e)
	}
	if _, err := os.Stat(filepath.Join(dir, "www", "daily", "2026-09-30.html")); err != nil {
		t.Error("daily page missing after update")
	}
	// A second update finds nothing new and rewrites only the index (which
	// shows the time of the latest import).
	code, out, _ = runCLI(t, "-config", cfg, "update")
	if code != exitOK || !strings.Contains(out, "pagine elaborate: 1, scritte: 1") {
		t.Errorf("second update: %d %s", code, out)
	}
	if code, out, _ = runCLI(t, "-config", cfg, "verify"); code != exitOK {
		t.Errorf("verify: %d\n%s", code, out)
	}
	code, out, _ = runCLI(t, "-config", cfg, "stats", "--json")
	var st struct {
		Metrics map[string]float64 `json:"metrics"`
	}
	if code != exitOK || json.Unmarshal([]byte(out), &st) != nil || st.Metrics["msg_accepted"] == 0 {
		t.Errorf("stats: %d %s", code, out)
	}
	for _, args := range [][]string{
		{"generate", "--date", "2026-10-01"}, {"generate", "--week", "2026-W40"},
		{"generate", "--month", "2026-09"}, {"generate", "--year", "2026"}, {"generate", "--all"},
	} {
		if code, out, e := runCLI(t, append([]string{"-config", cfg}, args...)...); code != exitOK {
			t.Errorf("%v: %d %s %s", args, code, out, e)
		}
	}
	if code, _, _ := runCLI(t, "-config", cfg, "rebuild"); code != exitOK {
		t.Errorf("rebuild: %d", code)
	}
	// Errors and exit codes.
	if code, _, _ := runCLI(t, "-config", cfg, "generate", "--date", "2026-13-01"); code != exitUsage {
		t.Errorf("bad date: %d", code)
	}
	if code, _, _ := runCLI(t, "-config", cfg, "generate", "--date", "2020-01-01"); code != exitError {
		t.Errorf("out of range date: %d", code)
	}
	if code, _, _ := runCLI(t, "-config", cfg, "frobnicate"); code != exitUsage {
		t.Errorf("unknown command: %d", code)
	}
	if code, _, _ := runCLI(t, "-config", filepath.Join(dir, "nope.yaml"), "update"); code != exitUsage {
		t.Errorf("missing config: %d", code)
	}
	if code, _, _ := runCLI(t, "-config", cfg, "import"); code != exitUsage {
		t.Errorf("import without files: %d", code)
	}
	if code, _, _ := runCLI(t, "-config", cfg, "import", "--file", filepath.Join(dir, "log", "missing.log")); code != exitPartial {
		t.Errorf("import of a missing file: %d", code)
	}
}

func TestCLIConcurrentRunIsRefused(t *testing.T) {
	cfgPath, _ := e2e(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(cfg, newLogger("error", &bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if code, _, _ := runCLI(t, "-config", cfgPath, "update"); code != exitLocked {
		t.Errorf("concurrent update: exit %d, want %d", code, exitLocked)
	}
}

func TestCLIImportDirectory(t *testing.T) {
	cfg, dir := e2e(t)
	code, out, e := runCLI(t, "-config", cfg, "import", "--directory", filepath.Join(dir, "log"))
	if code != exitOK {
		t.Fatalf("import --directory: %d %s %s", code, out, e)
	}
	// Importing the same files again through update adds nothing.
	code, out, _ = runCLI(t, "-config", cfg, "update")
	if code != exitOK || !strings.Contains(out, "eventi correlati: 0") {
		t.Errorf("update after import: %d %s", code, out)
	}
}

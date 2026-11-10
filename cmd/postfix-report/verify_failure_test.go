// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fdefilippo/postfix-report/internal/store"
)

func TestVerifyCLIFailsOnMissingDirtyState(t *testing.T) {
	cfg, dir := e2e(t)
	if code, out, errout := runCLI(t, "-config", cfg, "update"); code != exitOK {
		t.Fatalf("setup=%d %s %s", code, out, errout)
	}
	if code, out, errout := runCLI(t, "-config", cfg, "verify"); code != exitOK {
		t.Fatalf("baseline verify=%d %s %s", code, out, errout)
	}
	db, err := store.Open(filepath.Join(dir, "var/report.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE dirty_days`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, errout := runCLI(t, "-config", cfg, "verify")
	if code == exitOK {
		t.Fatalf("verify passed despite missing dirty_days: %s %s", out, errout)
	}
	if !strings.Contains(out+errout, "aggregati in attesa di ricalcolo") {
		t.Fatalf("missing named failed check: %s %s", out, errout)
	}
}

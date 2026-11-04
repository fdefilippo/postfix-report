// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIExportAndNoOverwrite(t *testing.T) {
	cfg, dir := e2e(t)
	if code, _, e := runCLI(t, "-config", cfg, "update"); code != exitOK {
		t.Fatal(code, e)
	}
	for format, signature := range map[string]string{"pdf": "%PDF-", "odt": "PK"} {
		p := filepath.Join(dir, "report."+format)
		args := []string{"-config", cfg, "export", "--month", "2026-10", "--format", format, "--output", p}
		if code, _, e := runCLI(t, args...); code != exitOK {
			t.Fatal(code, e)
		}
		b, err := os.ReadFile(p)
		if err != nil || !bytes.HasPrefix(b, []byte(signature)) {
			t.Fatalf("bad %s: %v", format, err)
		}
		if st, _ := os.Stat(p); st.Mode().Perm() != 0600 {
			t.Fatal("public export permissions")
		}
		if code, _, _ := runCLI(t, args...); code == exitOK {
			t.Fatal("existing output overwritten")
		}
		after, _ := os.ReadFile(p)
		if !bytes.Equal(b, after) {
			t.Fatal("existing file changed")
		}
	}
	if code, _, _ := runCLI(t, "-config", cfg, "export", "--year", "2026", "--date", "2026-10-01", "--format", "pdf", "--output", filepath.Join(dir, "bad.pdf")); code != exitUsage {
		t.Fatal("ambiguous period accepted")
	}
}

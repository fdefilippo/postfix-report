// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMigratesOnceAndRestrictsPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x", "r.db")
	for i := 0; i < 2; i++ {
		db, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		v, err := db.SchemaVersion()
		if err != nil || v != 1 {
			t.Errorf("version %d %v", v, err)
		}
		if err := SetMeta(db, "k", "v"); err != nil {
			t.Fatal(err)
		}
		if got, _ := GetMeta(db, "k"); got != "v" {
			t.Errorf("meta %q", got)
		}
		db.Close()
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o007 != 0 {
		t.Errorf("database is world-accessible: %v", st.Mode().Perm())
	}
}

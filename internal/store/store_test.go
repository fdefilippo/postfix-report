// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"database/sql"
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
		if err != nil || v != 2 {
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

func TestUpgradeFromV1PreservesReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at INTEGER NOT NULL);
 INSERT INTO schema_migrations VALUES(1,'0001_init.sql',0);
 INSERT INTO sources(id,fingerprint,fp_len,path,created_at,updated_at,offset) VALUES(41,'same',4,'log',0,0,4096);
 INSERT INTO imports(id,command,started_at,status) VALUES(1,'test',0,'ok');
 INSERT INTO import_files(import_id,source_id,path,start_offset,end_offset,lines,events,duplicates,non_postfix,malformed,unknown) VALUES(1,41,'log',0,4096,1,1,0,0,0,0);
 INSERT INTO segments(source_id,host,start_ts,end_ts) VALUES(41,'mx',1,2);
 INSERT INTO unknown_lines VALUES(41,'x',1,'synthetic');
 INSERT INTO events(source_id,line_offset,ukey,ts,host,inst,proc,kind) VALUES(41,0,1,1,'mx','','smtpd','connect');`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"sources", "events", "segments", "import_files", "unknown_lines"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s count=%d err=%v", table, n, err)
		}
	}
	var fk, broken int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("FK=%d %v", fk, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
		t.Fatalf("broken=%d %v", broken, err)
	}
	if _, err := db.Exec(`INSERT INTO sources(fingerprint,fp_len,path,created_at,updated_at) VALUES('same',4,'branch',0,0)`); err != nil {
		t.Fatal("shared head still constrained:", err)
	}
}

func TestMigrationRollbackRestoresForeignKeys(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.applyMigration(2, "invalid-fixture", `INSERT INTO segments(source_id,host,start_ts,end_ts) VALUES(999,'mx',1,2)`)
	if err == nil {
		t.Fatal("invalid FK migration accepted")
	}
	var n, fk int
	if err := db.QueryRow(`SELECT COUNT(*) FROM segments`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback: %d %v", n, err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("FK restoration: %d %v", fk, err)
	}
}

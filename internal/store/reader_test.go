// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReaderNeverCreatesMigratesOrWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "new.db")
	if db, err := OpenReader(context.Background(), p); err == nil {
		_ = db.Close()
		t.Fatal("created missing DB")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("reader created path: %v", err)
	}
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	r, err := OpenReader(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Exec(`INSERT INTO meta(key,value) VALUES('reader','bad')`); err == nil {
		t.Fatal("read-only write succeeded")
	}
	_ = r.Close()
	db, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM schema_migrations WHERE version=2`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if r, err = OpenReader(context.Background(), p); err == nil {
		_ = r.Close()
		t.Fatal("old schema silently accepted/migrated")
	}
}

func TestWALReaderRetainsSnapshotWhileWriterCommits(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db.sqlite")
	writer, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func(cleanup func() error) { _ = cleanup() }(writer.Close)
	if _, err = writer.Exec(`INSERT INTO meta(key,value) VALUES('snapshot','before')`); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer func(cleanup func() error) { _ = cleanup() }(r.Close)
	tx, err := r.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func(cleanup func() error) { _ = cleanup() }(tx.Rollback)
	var value string
	if err = tx.QueryRow(`SELECT value FROM meta WHERE key='snapshot'`).Scan(&value); err != nil || value != "before" {
		t.Fatal(value, err)
	}
	if _, err = writer.Exec(`UPDATE meta SET value='after' WHERE key='snapshot'`); err != nil {
		t.Fatal("writer blocked by reader", err)
	}
	if err = tx.QueryRow(`SELECT value FROM meta WHERE key='snapshot'`).Scan(&value); err != nil || value != "before" {
		t.Fatal("snapshot mixed revisions", value, err)
	}
}

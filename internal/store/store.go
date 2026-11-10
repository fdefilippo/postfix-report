// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package store owns the SQLite database: connection setup, versioned
// migrations and small helpers shared by the other layers.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB wraps the database handle.
type DB struct {
	*sql.DB
	Path string
}

// Open opens (creating if needed) the database and applies pending migrations.
func Open(path string) (*DB, error) {
	u, err := databaseURL(path)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Create with restrictive permissions: the DB contains personal data.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
	}
	params := u.Query()
	for _, pragma := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(ON)"} {
		params.Add("_pragma", pragma)
	}
	params.Set("_txlock", "immediate")
	u.RawQuery = params.Encode()
	sdb, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// One connection: the program is a single writer and this avoids
	// SQLITE_BUSY between connections of the same process.
	sdb.SetMaxOpenConns(1)
	db := &DB{DB: sdb, Path: path}
	if err := db.migrate(); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// databaseURL gives both readers and writers the same literal, absolute path.
func databaseURL(path string) (url.URL, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return url.URL{}, err
	}
	return url.URL{Scheme: "file", Path: abs}, nil
}

// LatestSchemaVersion returns the newest migration embedded in this binary.
func LatestSchemaVersion() (int, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return 0, err
	}
	want := 0
	for _, e := range entries {
		n, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return 0, fmt.Errorf("bad migration name %s: %w", e.Name(), err)
		}
		want = max(want, n)
	}
	return want, nil
}

// SchemaVersion returns the highest applied migration.
func (db *DB) SchemaVersion() (int, error) {
	var v sql.NullInt64
	err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	return int(v.Int64), err
}

func (db *DB) migrate() error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	cur, err := db.SchemaVersion()
	if err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		name := e.Name()
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration name %s", name)
		}
		if v <= cur {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if err := db.applyMigration(v, name, string(body)); err != nil {
			return err
		}

	}
	return nil
}

// Tx runs fn in a transaction.
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		// Preserve the callback failure; rollback is cleanup after that failure.
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

// GetMeta reads a meta value ("" if absent).
func GetMeta(q Execer, key string) (string, error) {
	var v string
	err := q.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetMeta writes a meta value.
func SetMeta(q Execer, key, value string) error {
	_, err := q.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// MarkDirtyDays records days whose aggregates must be recomputed.
func MarkDirtyDays(q Execer, days map[string]bool) error {
	for d := range days {
		if _, err := q.Exec(`INSERT OR IGNORE INTO dirty_days(day) VALUES(?)`, d); err != nil {
			return err
		}
	}
	return nil
}

// NullStr converts "" to NULL.
func NullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// applyMigration keeps schema/data changes atomic. Migration 2 uses SQLite's
// table-rebuild procedure, preserving referencing foreign keys and source IDs.
func (db *DB) applyMigration(v int, name, body string) (err error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func(cleanup func() error) { _ = cleanup() }(conn.Close)
	if v == 2 {
		if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
			return err
		}
		defer func() { _, restore := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err = errors.Join(err, restore) }()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func(cleanup func() error) { _ = cleanup() }(tx.Rollback)
	if _, err = tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if v == 2 {
		var broken int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil {
			return err
		}
		if broken != 0 {
			return fmt.Errorf("%s: %d foreign key violations", name, broken)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)`, v, name, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"syscall"
	"time"
)

// OpenReader opens an existing database without creating or migrating it.
// WAL is mutable: immutable=1 must never be used for this reader.
func OpenReader(ctx context.Context, path string) (*DB, error) {
	u, err := databaseURL(path)
	if err != nil {
		return nil, err
	}
	v := u.Query()
	v.Set("mode", "ro")
	v.Set("_txlock", "deferred")
	v.Add("_pragma", "query_only(1)")
	v.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = v.Encode()
	sdb, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(1)
	fail := func(err error) (*DB, error) { _ = sdb.Close(); return nil, err }
	if err = sdb.PingContext(ctx); err != nil {
		return fail(err)
	}
	var version int
	if err = sdb.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return fail(err)
	}
	want, err := LatestSchemaVersion()
	if err != nil {
		return fail(err)
	}
	if version != want {
		return fail(fmt.Errorf("database schema is %d, expected %d: run update with a matching binary", version, want))
	}
	return &DB{DB: sdb, Path: path}, nil
}

// ReadLock coordinates a short materialization with the CLI's exclusive lock.
// It never creates the lock file and it can be interrupted while waiting.
func ReadLock(ctx context.Context, path string) (func(), error) {
	f, err := os.Open(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("open import lock (run update first): %w", err)
	}
	timer := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

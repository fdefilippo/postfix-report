// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package app orchestrates the pipeline: import → correlate → aggregate →
// invalidate → generate. Each stage lives in its own package; this package
// only sequences them and records import runs.
package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/fdefilippo/postfix-report/internal/aggregate"
	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/correlate"
	"github.com/fdefilippo/postfix-report/internal/ingest"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
)

// ErrLocked is returned when another instance holds the lock.
var ErrLocked = errors.New("another postfix-report process is running")

// App holds the shared dependencies.
type App struct {
	Cfg  *config.Config
	DB   *store.DB
	Log  *slog.Logger
	lock *os.File
}

// Open acquires the process lock and opens the database.
func Open(cfg *config.Config, log *slog.Logger) (*App, error) {
	lockPath := cfg.Database.Path + ".lock"
	if err := os.MkdirAll(dirOf(lockPath), 0o750); err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lf.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		lf.Close()
		return nil, err
	}
	return &App{Cfg: cfg, DB: db, Log: log, lock: lf}, nil
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			if i == 0 {
				return "/"
			}
			return p[:i]
		}
	}
	return "."
}

// Close releases the database and the lock.
func (a *App) Close() error {
	err := a.DB.Close()
	if a.lock != nil {
		syscall.Flock(int(a.lock.Fd()), syscall.LOCK_UN)
		a.lock.Close()
	}
	return err
}

// ImportSummary reports what an import changed.
type ImportSummary struct {
	Files       []ingest.FileResult
	Lines       int64
	Events      int64
	Duplicates  int64
	Correlated  int64
	Reinjected  int64
	Orphans     int64
	FullHosts   []string
	DaysUpdated []string
	FileErrors  error
}

// Import imports files, correlates new events, recomputes affected days and
// invalidates the affected report pages.
func (a *App) Import(ctx context.Context, command string, paths []string) (*ImportSummary, error) {
	r, err := a.DB.Exec(`INSERT INTO imports(command, started_at, status) VALUES(?,?, 'running')`, command, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	importID, _ := r.LastInsertId()
	sum, err := a.doImport(ctx, importID, paths)
	status, msg := "ok", ""
	if err != nil {
		status, msg = "failed", err.Error()
	} else if sum.FileErrors != nil {
		status, msg = "partial", sum.FileErrors.Error()
	}
	var files, lines, events, dups int64
	if sum != nil {
		files, lines, events, dups = int64(len(sum.Files)), sum.Lines, sum.Events, sum.Duplicates
	}
	if _, uerr := a.DB.Exec(`UPDATE imports SET finished_at=?, status=?, files=?, lines=?, events=?, duplicates=?, error=? WHERE id=?`,
		time.Now().Unix(), status, files, lines, events, dups, store.NullStr(msg), importID); uerr != nil && err == nil {
		err = uerr
	}
	return sum, err
}

func (a *App) doImport(ctx context.Context, importID int64, paths []string) (*ImportSummary, error) {
	im := &ingest.Importer{DB: a.DB, Cfg: a.Cfg, Log: a.Log}
	res, ferr := im.Import(ctx, importID, paths)
	if res == nil {
		return nil, ferr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	sum := &ImportSummary{Files: res.Files, Lines: res.Lines, Events: res.Events, Duplicates: res.Duplicates, FileErrors: ferr}
	co := &correlate.Correlator{DB: a.DB, Cfg: a.Cfg, Log: a.Log}
	cs, err := co.Run(ctx, res.MinTS)
	if err != nil {
		return sum, fmt.Errorf("correlate: %w", err)
	}
	sum.Correlated, sum.Reinjected, sum.Orphans, sum.FullHosts = cs.Events, cs.Reinjected, cs.Orphans, cs.FullHosts
	days := map[string]bool{}
	for d := range res.DirtyDays {
		days[d] = true
	}
	for d := range cs.DirtyDays {
		days[d] = true
	}
	if err := store.MarkDirtyDays(a.DB, days); err != nil {
		return sum, err
	}
	sum.DaysUpdated, err = a.Aggregate(ctx)
	return sum, err
}

// Aggregate recomputes the dirty days and invalidates their pages.
func (a *App) Aggregate(ctx context.Context) ([]string, error) {
	ag := &aggregate.Aggregator{DB: a.DB, Loc: a.Cfg.Location}
	days, err := ag.RecomputeDirty(ctx)
	if err != nil {
		return days, fmt.Errorf("aggregate: %w", err)
	}
	return days, a.invalidate(days)
}

// invalidate marks the report pages that depend on the given days: the
// periods containing them, their neighbours (navigation links and
// comparisons with the previous period) and the index.
func (a *App) invalidate(days []string) error {
	if len(days) == 0 {
		return nil
	}
	pages := map[[2]string]bool{{"index", "index"}: true}
	for _, d := range days {
		t, err := time.ParseInLocation("2006-01-02", d, a.Cfg.Location)
		if err != nil {
			return err
		}
		for _, k := range period.Kinds {
			p := period.Of(k, t, a.Cfg.Location)
			for _, q := range []period.Period{p, p.Prev(), p.Next()} {
				pages[[2]string{string(k), q.Key}] = true
			}
		}
	}
	return a.DB.Tx(context.Background(), func(tx *sql.Tx) error {
		for k := range pages {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO dirty_pages(kind, key) VALUES(?,?)`, k[0], k[1]); err != nil {
				return err
			}
		}
		return nil
	})
}

// Rebuild re-correlates all stored events and recomputes every aggregate.
func (a *App) Rebuild(ctx context.Context) (*correlate.Stats, []string, error) {
	co := &correlate.Correlator{DB: a.DB, Cfg: a.Cfg, Log: a.Log}
	cs, err := co.Rebuild(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err := a.DB.Exec(`DELETE FROM agg_hour`); err != nil {
		return cs, nil, err
	}
	if _, err := a.DB.Exec(`DELETE FROM agg_dim`); err != nil {
		return cs, nil, err
	}
	if err := store.MarkDirtyDays(a.DB, cs.DirtyDays); err != nil {
		return cs, nil, err
	}
	days, err := a.Aggregate(ctx)
	return cs, days, err
}

// AnonKey returns the secret key used to pseudonymize report data, creating
// it on first use. It is stored only in the database.
func (a *App) AnonKey() ([]byte, error) {
	v, err := store.GetMeta(a.DB, "anon_key")
	if err != nil {
		return nil, err
	}
	if v == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		v = hex.EncodeToString(b)
		if err := store.SetMeta(a.DB, "anon_key", v); err != nil {
			return nil, err
		}
	}
	return hex.DecodeString(v)
}

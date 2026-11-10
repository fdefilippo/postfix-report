// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package exporter materializes consistent reports for CLI and web consumers.
package exporter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/document"
	"github.com/fdefilippo/postfix-report/internal/norm"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/query"
	"github.com/fdefilippo/postfix-report/internal/report"
	"github.com/fdefilippo/postfix-report/internal/store"
)

// PolicyKey invalidates jobs whenever effective output or privacy settings change.
func PolicyKey(c *config.Config) string {
	b, _ := json.Marshal(c)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Prepare reads a complete period while coordinated with the writer. No lock or
// database transaction survives this call; the renderer only sees presentation data.
func Prepare(ctx context.Context, cfg *config.Config, p period.Period, version string) (document.Document, error) {
	var d document.Document
	err := withGenerator(ctx, cfg, version, func(g *report.Generator) error {
		var err error
		d, err = g.Document(ctx, p)
		return err
	})
	return d, err
}

// withGenerator pins one completed WAL snapshot for all queries in fn. The
// process lock is released before queries, and the transaction before rendering.
func withGenerator(ctx context.Context, cfg *config.Config, version string, fn func(*report.Generator) error) error {
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := store.ReadLock(readCtx, cfg.Database.Path)
	if err != nil {
		return err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	db, err := store.OpenReader(readCtx, cfg.Database.Path)
	if err != nil {
		return err
	}
	defer func(cleanup func() error) { _ = cleanup() }(db.Close)
	tx, err := db.BeginTx(readCtx, nil)
	if err != nil {
		return err
	}
	defer func(cleanup func() error) { _ = cleanup() }(tx.Rollback)
	var dirty int
	if err = tx.QueryRowContext(readCtx, `SELECT COUNT(*) FROM dirty_days`).Scan(&dirty); err != nil {
		return err
	}
	if dirty > 0 {
		return errors.New("aggregates are pending: run update before exporting")
	}
	var status string
	err = tx.QueryRowContext(readCtx, `SELECT status FROM imports ORDER BY id DESC LIMIT 1`).Scan(&status)
	if err != nil {
		return err
	}
	if status == "running" || status == "failed" {
		return fmt.Errorf("latest import is %s: run update before exporting", status)
	}
	var key string
	if err = tx.QueryRowContext(readCtx, `SELECT value FROM meta WHERE key='anon_key'`).Scan(&key); err != nil {
		return fmt.Errorf("anonymization key not initialized: run update first: %w", err)
	}
	secret, err := hex.DecodeString(key)
	if err != nil || len(secret) != 32 {
		return errors.New("invalid anonymization key")
	}
	// The first reads pin a WAL snapshot from a completed writer run.
	// Release the process lock before the potentially expensive report queries;
	// all of them still read the same transaction while update can proceed.
	unlock()
	unlock = nil
	q := &query.Q{DB: query.ContextReader(readCtx, tx), Loc: cfg.Location, Gap: int64(cfg.Coverage.GapThreshold.Seconds()), MaxDelaySamples: 1_000_000}
	g := &report.Generator{Cfg: cfg, Q: q, Anon: norm.NewAnonymizer(secret, cfg.Privacy.AnonymizeEmail, cfg.Privacy.AnonymizeIP), Version: version}
	if err = g.Init(); err != nil {
		return err
	}
	return fn(g)
}

// Render is shared by the HTTP worker and the one-shot CLI export.
func Render(ctx context.Context, cfg *config.Config, p period.Period, format, version string) ([]byte, error) {
	d, err := Prepare(ctx, cfg, p, version)
	if err != nil {
		return nil, err
	}
	return document.Render(ctx, d, format)
}

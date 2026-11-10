// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package exporter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/document"
	"github.com/fdefilippo/postfix-report/internal/report"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

func TestArchiveSnapshotSurvivesWriterCommit(t *testing.T) {
	e := testutil.NewEnv(t)
	l := testutil.NewLog(t)
	l.SMTPMessage("2026-10-25 10:00:00", "ABC01", "DEF02", "alice@example.org", 123, "bob@example.net")
	testutil.WriteFile(t, e.LogPath("maillog"), l.String(), time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	a := e.Open(t)
	if _, err := a.Import(context.Background(), "test", []string{e.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AnonKey(); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var d document.Document
	err := withGenerator(ctx, e.Cfg, "snapshot-test", func(g *report.Generator) error {
		plan, periods, err := planArchive(ctx, e.Cfg, g, ArchiveSelection{})
		if err != nil {
			return err
		}
		// The archive snapshot is already pinned. A writer must still obtain
		// its process lock and commit without waiting for these report queries.
		writer, err := app.Open(e.Cfg, e.Log)
		if err != nil {
			return err
		}
		_, err = writer.DB.Exec(`UPDATE agg_hour SET value=value+10 WHERE metric='msg_accepted'`)
		_ = writer.Close()
		if err != nil {
			return err
		}
		d, err = g.ArchiveDocument(ctx, periods, plan.From, plan.To)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	count := func(d document.Document, text string) int {
		n := 0
		for _, b := range d.Blocks {
			if strings.HasPrefix(b.Text, text) {
				n++
			}
		}
		return n
	}
	if n := count(d, "Messaggi accettati: 1 ·"); n != 4 {
		t.Fatalf("snapshot mixed old/new metrics: %d", n)
	}
	after, err := PrepareArchive(ctx, e.Cfg, ArchiveSelection{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if count(after, "Messaggi accettati: 11 ·") != 4 {
		t.Fatal("negative control: writer update not visible in next snapshot")
	}
}

// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/norm"
	"github.com/fdefilippo/postfix-report/internal/query"
)

// benchmarkArchive uses the real import/correlate/aggregate pipeline, with one
// fully observed SMTP message per UTC day. It measures archive length, not high
// traffic/cardinality, and creates no period pages (only the index is relevant).
func benchmarkArchive(b *testing.B, years int) (*app.App, *Generator, string) {
	b.Helper()
	root := b.TempDir()
	c := config.Default()
	c.Database.Path = filepath.Join(root, "report.db")
	c.Output.Directory = filepath.Join(root, "www")
	c.Input.Timezone = "UTC"
	c.Location = time.UTC
	c.Reports.Daily, c.Reports.Weekly, c.Reports.Monthly, c.Reports.Yearly = false, false, false, false
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(years, 0, 0)
	var logs bytes.Buffer
	for d, i := start, 0; d.Before(end); d, i = d.AddDate(0, 0, 1), i+1 {
		qid := fmt.Sprintf("A%010X", i)
		for j, entry := range []struct{ agent, text string }{
			{"smtpd", "client=app.example.org[192.0.2.10]"},
			{"cleanup", fmt.Sprintf("message-id=<message%d@example.org>", i)},
			{"qmgr", "from=<sender@example.org>, size=100, nrcpt=1 (queue active)"},
			{"smtp", "to=<recipient@example.net>, relay=mx.example.net[198.51.100.1]:25, delay=1, delays=0.1/0.1/0.1/0.7, dsn=2.0.0, status=sent (OK)"},
			{"qmgr", "removed"},
		} {
			_, _ = fmt.Fprintf(&logs, "%s mx1 postfix/%s[1]: %s: %s\n", d.Add(12*time.Hour+time.Duration(j)*time.Second).Format(time.RFC3339), entry.agent, qid, entry.text)
		}
	}
	path := filepath.Join(root, "maillog")
	if err := os.WriteFile(path, logs.Bytes(), 0600); err != nil {
		b.Fatal(err)
	}
	a, err := app.Open(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := a.Close(); err != nil {
			b.Error(err)
		}
	})
	if _, err := a.Import(context.Background(), "benchmark", []string{path}); err != nil {
		b.Fatal(err)
	}
	key, err := a.AnonKey()
	if err != nil {
		b.Fatal(err)
	}
	g := &Generator{DB: a.DB, Cfg: c, Q: &query.Q{DB: a.DB, Loc: time.UTC, Gap: 3600}, Anon: norm.NewAnonymizer(key, false, false), Version: "benchmark"}
	if err := g.Init(); err != nil {
		b.Fatal(err)
	}
	if !g.firstDay.Start.Equal(start) || !g.lastDay.End.Equal(end) {
		b.Fatalf("archive range %s..%s, expected %s..%s", g.firstDay.Start, g.lastDay.End, start, end)
	}
	if _, err := g.GenerateAll(context.Background()); err != nil {
		b.Fatal(err)
	}
	return a, g, path
}

func BenchmarkArchiveIndex(b *testing.B) {
	for _, years := range []int{3, 5} {
		b.Run(fmt.Sprintf("years%d", years), func(b *testing.B) {
			_, g, _ := benchmarkArchive(b, years)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := g.indexPage(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// NoNewDataUpdate includes import/correlation/dirty checking, index SQL,
// template rendering and hash comparison, as update does; setup is untimed.
// It excludes CLI process startup and the application lock acquired once above.
func BenchmarkNoNewDataUpdate(b *testing.B) {
	for _, years := range []int{3, 5} {
		b.Run(fmt.Sprintf("years%d", years), func(b *testing.B) {
			a, g, path := benchmarkArchive(b, years)
			b.ReportAllocs()
			for b.Loop() {
				sum, err := a.Import(context.Background(), "benchmark", []string{path})
				if err != nil {
					b.Fatal(err)
				}
				if sum.FileErrors != nil || sum.Events != 0 || sum.Lines != 0 {
					b.Fatalf("new data in unchanged source: %+v", sum)
				}
				if _, err := g.GenerateDirty(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

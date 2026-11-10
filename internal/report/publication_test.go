// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package report_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/report"
	"github.com/fdefilippo/postfix-report/internal/store"
	"github.com/fdefilippo/postfix-report/internal/testutil"
	"github.com/fdefilippo/postfix-report/internal/verify"
)

func publicationFixture(t *testing.T) (*testutil.Env, *app.App, *report.Generator) {
	t.Helper()
	e := testutil.NewEnv(t)
	l := testutil.NewLog(t)
	l.SMTPMessage("2026-01-31 10:00:00", "ABC01", "DEF02", "sender@example.org", 42, "recipient@example.net")
	l.SMTPMessage("2026-02-01 10:00:00", "ABC03", "DEF04", "sender@example.org", 42, "recipient@example.net")
	p := e.LogPath("mail")
	testutil.WriteFile(t, p, l.String(), time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC))
	a := e.Open(t)
	t.Cleanup(func() { _ = a.Close() })
	if _, err := a.Import(ctx, "test", []string{p}); err != nil {
		t.Fatal(err)
	}
	g := newGen(t, e, a)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	return e, a, g
}

func setReportEnabled(e *testutil.Env, k period.Kind, enabled bool) {
	switch k {
	case period.Day:
		e.Cfg.Reports.Daily = enabled
	case period.Week:
		e.Cfg.Reports.Weekly = enabled
	case period.Month:
		e.Cfg.Reports.Monthly = enabled
	case period.Year:
		e.Cfg.Reports.Yearly = enabled
	}
}

func checkPublishedLinks(t *testing.T, e *testutil.Env) {
	t.Helper()
	links, err := verify.CheckLinks(e.Cfg.Output.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(links.Broken) != 0 || len(links.Temp) != 0 {
		t.Fatalf("broken=%v temporary=%v", links.Broken, links.Temp)
	}
}

func TestDisablingReportsWithdrawsRegisteredPages(t *testing.T) {
	for _, mode := range []string{"all", "dirty", "one"} {
		for _, kind := range period.Kinds {
			t.Run(mode+"/"+string(kind), func(t *testing.T) {
				e, a, g := publicationFixture(t)
				pages := g.AllPages()
				custom := filepath.Join(e.Cfg.Output.Directory, "custom.txt")
				if err := os.WriteFile(custom, []byte("unmanaged"), 0600); err != nil {
					t.Fatal(err)
				}
				setReportEnabled(e, kind, false)
				g = newGen(t, e, a)
				var err error
				switch mode {
				case "all":
					_, err = g.GenerateAll(ctx)
				case "dirty":
					_, err = g.GenerateDirty(ctx)
				case "one":
					_, err = g.GenerateOne(g.AllPages()[0])
				}
				if err != nil {
					t.Fatal(err)
				}
				var n int
				if err := a.DB.QueryRow(`SELECT COUNT(*) FROM report_pages WHERE kind=?`, string(kind)).Scan(&n); err != nil || n != 0 {
					t.Fatalf("disabled records=%d %v", n, err)
				}
				for _, p := range pages {
					_, err := os.Stat(filepath.Join(e.Cfg.Output.Directory, p.Path()))
					if p.Kind == kind {
						if !os.IsNotExist(err) {
							t.Fatalf("disabled page remains: %s: %v", p.Path(), err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
				}
				if body, err := os.ReadFile(custom); err != nil || string(body) != "unmanaged" {
					t.Fatalf("custom file changed: %q %v", body, err)
				}
				checkPublishedLinks(t, e)
				// Enabling a type again must restore the archive even without new logs.
				setReportEnabled(e, kind, true)
				g = newGen(t, e, a)
				if _, err := g.GenerateDirty(ctx); err != nil {
					t.Fatal(err)
				}
				for _, p := range pages {
					if _, err := os.Stat(filepath.Join(e.Cfg.Output.Directory, p.Path())); err != nil {
						t.Fatal(err)
					}
				}
				checkPublishedLinks(t, e)
			})
		}
	}
}

func TestWithdrawalRetryAfterPartialDeletion(t *testing.T) {
	e, a, _ := publicationFixture(t)
	old, err := store.GetMeta(a.DB, "report_publication")
	if err != nil {
		t.Fatal(err)
	}
	setReportEnabled(e, period.Month, false)
	blocked := filepath.Join(e.Cfg.Output.Directory, "monthly/2026-02.html")
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	g := newGen(t, e, a)
	if _, err := g.GenerateDirty(ctx); err == nil {
		t.Fatal("expected blocked retirement")
	}
	if _, err := os.Stat(filepath.Join(e.Cfg.Output.Directory, "monthly/2026-01.html")); !os.IsNotExist(err) {
		t.Fatal("first page was not retired before failure", err)
	}
	marker, err := store.GetMeta(a.DB, "report_publication")
	if err != nil || marker != old {
		t.Fatalf("premature acknowledgement %q %v", marker, err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GenerateDirty(ctx); err != nil {
		t.Fatal(err)
	}
	checkPublishedLinks(t, e)
	var n int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM report_pages WHERE kind='month'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("retired records=%d %v", n, err)
	}
}

func TestWithdrawalRejectsInvalidRegisteredPath(t *testing.T) {
	e, a, _ := publicationFixture(t)
	custom := filepath.Join(e.Dir, "outside.html")
	if err := os.WriteFile(custom, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(`UPDATE report_pages SET path='../outside.html' WHERE kind='month' AND key='2026-01'`); err != nil {
		t.Fatal(err)
	}
	setReportEnabled(e, period.Month, false)
	if _, err := newGen(t, e, a).GenerateDirty(ctx); err == nil {
		t.Fatal("accepted invalid registered path")
	}
	if body, err := os.ReadFile(custom); err != nil || string(body) != "keep" {
		t.Fatalf("outside changed %q %v", body, err)
	}
}

func TestCancelledWithdrawalKeepsPublicationMarker(t *testing.T) {
	e, a, _ := publicationFixture(t)
	old, err := store.GetMeta(a.DB, "report_publication")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range period.Kinds {
		setReportEnabled(e, k, false)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	g := newGen(t, e, a)
	if _, err := g.GenerateDirty(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	marker, err := store.GetMeta(a.DB, "report_publication")
	if err != nil || marker != old {
		t.Fatalf("premature marker %q %v", marker, err)
	}
	// Model interruption after every registered period was physically retired.
	// The index remains registered and must still be rewritten on retry.
	rows, err := a.DB.Query(`SELECT path FROM report_pages WHERE kind!='index'`)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if err := os.Remove(filepath.Join(e.Cfg.Output.Directory, p)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.DB.Exec(`DELETE FROM report_pages WHERE kind!='index'`); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GenerateDirty(ctx); err != nil {
		t.Fatal(err)
	}
	checkPublishedLinks(t, e)
}

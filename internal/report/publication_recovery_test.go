// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package report_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
)

func TestWithdrawalFailureThenConfigurationRollbackRecoversArchive(t *testing.T) {
	for _, mode := range []string{"dirty", "one"} {
		t.Run(mode, func(t *testing.T) {
			e, a, g := publicationFixture(t)
			pages := g.AllPages()
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
			if _, err := newGen(t, e, a).GenerateDirty(ctx); err == nil {
				t.Fatal("expected partial retirement failure")
			}
			if _, err := os.Stat(filepath.Join(e.Cfg.Output.Directory, "monthly/2026-01.html")); !os.IsNotExist(err) {
				t.Fatalf("first month not retired: %v", err)
			}
			marker, err := store.GetMeta(a.DB, "report_publication")
			if err != nil || marker != old {
				t.Fatalf("premature marker %q %v", marker, err)
			}
			if err := os.Remove(blocked); err != nil {
				t.Fatal(err)
			}
			// Restoring the previously acknowledged settings must not skip recovery:
			// one deleted page no longer has a report_pages record at this point.
			setReportEnabled(e, period.Month, true)
			g = newGen(t, e, a)
			if mode == "one" {
				_, err = g.GenerateOne(g.AllPages()[0])
			} else {
				_, err = g.GenerateDirty(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range pages {
				if _, err := os.Stat(filepath.Join(e.Cfg.Output.Directory, p.Path())); err != nil {
					t.Fatal(err)
				}
			}
			checkPublishedLinks(t, e)
			pending, err := store.GetMeta(a.DB, "report_publication_pending")
			if err != nil || pending != "" {
				t.Fatalf("pending %q %v", pending, err)
			}
		})
	}
}

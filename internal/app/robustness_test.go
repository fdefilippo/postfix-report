// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ag "github.com/fdefilippo/postfix-report/internal/aggregate"
	"github.com/fdefilippo/postfix-report/internal/ingest"
	"github.com/fdefilippo/postfix-report/internal/norm"
	"github.com/fdefilippo/postfix-report/internal/query"
	"github.com/fdefilippo/postfix-report/internal/report"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

// An earlier run stored old events but stopped before correlating them
// (crash, SIGTERM): the next run must still correlate them and recompute
// their days.
func TestInterruptedRunIsRecovered(t *testing.T) {
	lines := scenario(t).Lines()
	half := len(lines) / 2
	env := testutil.NewEnv(t)
	mt := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	testutil.WriteFile(t, env.LogPath("maillog"), strings.Join(lines[half:], "\n")+"\n", mt)
	testutil.WriteFile(t, env.LogPath("maillog.old"), strings.Join(lines[:half], "\n")+"\n", mt)
	testutil.WriteFile(t, env.LogPath("maillog.empty"), "", mt)
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	// Simulated crash: only the ingest stage runs for the old file.
	a.DB.Exec(`INSERT INTO imports(command, started_at, status) VALUES('crashed', 0, 'running')`)
	im := &ingest.Importer{DB: a.DB, Cfg: env.Cfg, Log: env.Log}
	if _, err := im.Import(ctx, 2, []string{env.LogPath("maillog.old")}); err != nil {
		t.Fatal(err)
	}
	// Next normal run, with nothing new to import.
	sum, err := a.Import(ctx, "update", []string{env.LogPath("maillog"), env.LogPath("maillog.old")})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.FullHosts) != 1 {
		t.Errorf("expected recovery by full re-correlation, got %v", sum.FullHosts)
	}
	expect(t, totals(t, a, "2026-10-12"), day12)
}

// Identical lines of one host interleaved with lines of another host (with a
// different second) are distinct lines, not duplicates.
func TestInterleavedHostsKeepIdenticalLines(t *testing.T) {
	content := strings.Join([]string{
		"Oct 12 10:00:00 mx1 postfix/smtpd[1]: warning: unknown[203.0.113.5]: SASL LOGIN authentication failed: UGFzc3dvcmQ6",
		"Oct 12 09:59:59 mx2 postfix/smtpd[2]: connect from unknown[192.0.2.1]",
		"Oct 12 10:00:00 mx1 postfix/smtpd[1]: warning: unknown[203.0.113.5]: SASL LOGIN authentication failed: UGFzc3dvcmQ6",
	}, "\n") + "\n"
	env := testutil.NewEnv(t)
	testutil.WriteFile(t, env.LogPath("maillog"), content, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	a := env.Open(t)
	defer a.Close()
	sum, err := a.Import(ctx, "test", []string{env.LogPath("maillog")})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Duplicates != 0 {
		t.Errorf("%d lines wrongly treated as duplicates", sum.Duplicates)
	}
	expect(t, totals(t, a, "2026-10-12"), map[string]float64{ag.MSASLFail: 2, ag.MConnections: 1})
}

// When new data extends the range past a gap, the pages of the gap days are
// created (as "missing data" pages) so that no link is broken.
func TestRangeGrowthCreatesGapPages(t *testing.T) {
	env := testutil.NewEnv(t)
	path := env.LogPath("maillog")
	mt := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	testutil.WriteFile(t, path, "Oct  5 10:00:00 mx1 postfix/smtpd[1]: connect from unknown[192.0.2.1]\n", mt)
	a := env.Open(t)
	defer a.Close()
	gen := func() {
		if _, err := a.Import(ctx, "test", []string{path}); err != nil {
			t.Fatal(err)
		}
		key, _ := a.AnonKey()
		g := &report.Generator{DB: a.DB, Cfg: env.Cfg, Version: "t",
			Q:    &query.Q{DB: a.DB, Loc: env.Cfg.Location, Gap: 3600},
			Anon: norm.NewAnonymizer(key, false, false)}
		if err := g.Init(); err != nil {
			t.Fatal(err)
		}
		if _, err := g.GenerateDirty(ctx); err != nil {
			t.Fatal(err)
		}
	}
	gen()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("Oct  9 10:00:00 mx1 postfix/smtpd[1]: connect from unknown[192.0.2.1]\n")
	f.Close()
	gen()
	for _, d := range []string{"05", "06", "07", "08", "09"} {
		if _, err := os.Stat(filepath.Join(env.Cfg.Output.Directory, "daily", "2026-10-"+d+".html")); err != nil {
			t.Errorf("page for 2026-10-%s missing", d)
		}
	}
}

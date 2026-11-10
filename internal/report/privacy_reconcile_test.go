// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package report_test

import (
	"context"
	"errors"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
	"github.com/fdefilippo/postfix-report/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivacyReconcilesDisabledArchive(t *testing.T) {
	for _, mode := range []string{"dirty", "all", "one"} {
		for _, kind := range period.Kinds {
			t.Run(mode+"/"+string(kind), func(t *testing.T) {
				env := testutil.NewEnv(t)
				a := env.Open(t)
				defer func(cleanup func() error) { _ = cleanup() }(a.Close)
				l := testutil.NewLog(t).Add("2026-10-12 10:00:00", "smtpd", "A111111111: client=app.example.org[192.0.2.123], sasl_method=LOGIN, sasl_username=private-user").Add("2026-10-12 10:00:00", "qmgr", "A111111111: from=<sender@example.org>, size=100, nrcpt=1 (queue active)")
				path := env.LogPath("mail")
				testutil.WriteFile(t, path, l.String(), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
				if _, err := a.Import(ctx, "test", []string{path}); err != nil {
					t.Fatal(err)
				}
				g := newGen(t, env, a)
				if _, err := g.GenerateAll(ctx); err != nil {
					t.Fatal(err)
				}
				pages := g.AllPages()
				p := period.Of(kind, time.Date(2026, 10, 12, 0, 0, 0, 0, env.Cfg.Location), env.Cfg.Location)
				if !strings.Contains(read(t, env, p.Path()), "private-user") {
					t.Fatal("baseline private user absent")
				}
				switch kind {
				case period.Day:
					env.Cfg.Reports.Daily = false
				case period.Week:
					env.Cfg.Reports.Weekly = false
				case period.Month:
					env.Cfg.Reports.Monthly = false
				case period.Year:
					env.Cfg.Reports.Yearly = false
				}
				unrelated := filepath.Join(env.Cfg.Output.Directory, "custom.txt")
				if err := os.WriteFile(unrelated, []byte("private-user"), 0600); err != nil {
					t.Fatal(err)
				}
				env.Cfg.Privacy.AnonymizeEmail = true
				env.Cfg.Privacy.AnonymizeIP = true
				g = newGen(t, env, a)
				if mode != "one" {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					var err error
					if mode == "all" {
						_, err = g.GenerateAll(cancelled)
					} else {
						_, err = g.GenerateDirty(cancelled)
					}
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel: %v", err)
					}
				}
				var err error
				switch mode {
				case "all":
					_, err = g.GenerateAll(ctx)
				case "dirty":
					_, err = g.GenerateDirty(ctx)
				case "one":
					for _, candidate := range g.AllPages() {
						_, err = g.GenerateOne(candidate)
						break
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range pages {
					if p.Kind == kind {
						if _, err := os.Stat(filepath.Join(env.Cfg.Output.Directory, p.Path())); !os.IsNotExist(err) {
							t.Fatalf("disabled page remains: %s (%v)", p.Path(), err)
						}
						continue
					}
					body := read(t, env, p.Path())
					if strings.Contains(body, "private-user") || strings.Contains(body, "192.0.2.123") {
						t.Errorf("unmasked managed page %s", p.Path())
					}
				}
				body, err := os.ReadFile(unrelated)
				if err != nil || string(body) != "private-user" {
					t.Fatal("unmanaged file changed")
				}
			})
		}
	}
}

func TestPrivacyRetryAfterPartialWriteFailure(t *testing.T) {
	env, a, g := fixture(t)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	pages := g.AllPages()
	old, err := store.GetMeta(a.DB, "report_privacy")
	if err != nil {
		t.Fatal(err)
	}
	// A directory at a managed weekly destination forces a rename failure
	// after some daily pages have already been rewritten successfully.
	blocked := filepath.Join(env.Cfg.Output.Directory, "weekly", "2026-W01.html")
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	env.Cfg.Reports.Weekly = false
	env.Cfg.Privacy.AnonymizeEmail = true
	env.Cfg.Privacy.AnonymizeIP = true
	g = newGen(t, env, a)
	if _, err := g.GenerateDirty(ctx); err == nil {
		t.Fatal("expected write failure")
	}
	marker, err := store.GetMeta(a.DB, "report_privacy")
	if err != nil || marker != old {
		t.Fatalf("privacy acknowledged before archive completed: %q %v", marker, err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GenerateDirty(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range pages {
		if p.Kind == period.Week {
			continue
		}
		body := read(t, env, p.Path())
		if strings.Contains(body, "192.0.2.10") {
			t.Errorf("raw IP after retry: %s", p.Path())
		}
	}
	// Disabling every type must still reconcile the registered archive.
	env.Cfg.Reports.Daily = false
	env.Cfg.Reports.Monthly = false
	env.Cfg.Reports.Yearly = false
	env.Cfg.Privacy.AnonymizeEmail = false
	g = newGen(t, env, a)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range pages {
		if _, err := os.Stat(filepath.Join(env.Cfg.Output.Directory, p.Path())); !os.IsNotExist(err) {
			t.Fatalf("disabled page remains: %s (%v)", p.Path(), err)
		}
	}
}

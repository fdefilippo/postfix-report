// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/norm"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/query"
	"github.com/fdefilippo/postfix-report/internal/report"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

func privacyRecoveryGenerator(t *testing.T, e *testutil.Env, cfg *config.Config) (*report.Generator, func()) {
	t.Helper()
	a := e.Open(t)
	key, err := a.AnonKey()
	if err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	g := &report.Generator{DB: a.DB, Q: &query.Q{DB: a.DB, Loc: cfg.Location, Gap: 3600}, Cfg: cfg, Anon: norm.NewAnonymizer(key, cfg.Privacy.AnonymizeEmail, cfg.Privacy.AnonymizeIP), Version: "test"}
	if err := g.Init(); err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	return g, func() {
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPendingPrivacyRollbackNeverServesPartiallyRawArchive(t *testing.T) {
	e, _, ts, ptr := serverFixture(t, "/postfix-report/")
	strict := *e.Cfg
	strict.Privacy.AnonymizeEmail = true
	strict.Privacy.AnonymizeIP = true
	g, closeApp := privacyRecoveryGenerator(t, e, &strict)
	if _, err := g.GenerateAll(context.Background()); err != nil {
		closeApp()
		t.Fatal(err)
	}
	closeApp()
	ptr.Store(&strict)
	b := newBrowser(t, ts, "/postfix-report/")
	if code, body := b.call("GET", "/daily/2026-10-25.html", nil); code != 200 || strings.Contains(string(body), "192.0.2.10") {
		t.Fatalf("masked baseline code=%d", code)
	}

	loose := strict
	loose.Privacy.AnonymizeEmail = false
	loose.Privacy.AnonymizeIP = false
	blocked := filepath.Join(e.Cfg.Output.Directory, "weekly/2026-W43.html")
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	g, closeApp = privacyRecoveryGenerator(t, e, &loose)
	if _, err := g.GenerateDirty(context.Background()); err == nil {
		closeApp()
		t.Fatal("expected write failure after daily raw page")
	}
	closeApp()
	raw, err := os.ReadFile(filepath.Join(e.Cfg.Output.Directory, "daily/2026-10-25.html"))
	if err != nil || !strings.Contains(string(raw), "192.0.2.10") {
		t.Fatalf("partial raw page not produced: %v", err)
	}
	// The acknowledged privacy marker again matches strict, but a prior failed
	// loosening wrote a raw page. The durable pending marker must deny serving it.
	if code, _ := b.call("GET", "/daily/2026-10-25.html", nil); code != 503 {
		t.Fatalf("pending rollback served HTTP%d", code)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	g, closeApp = privacyRecoveryGenerator(t, e, &strict)
	p := period.Of(period.Day, time.Date(2026, 10, 25, 0, 0, 0, 0, strict.Location), strict.Location)
	if _, err := g.GenerateOne(p); err != nil {
		closeApp()
		t.Fatal(err)
	}
	closeApp()
	if code, body := b.call("GET", "/daily/2026-10-25.html", nil); code != 200 || strings.Contains(string(body), "192.0.2.10") {
		t.Fatalf("recovered code=%d raw=%v", code, strings.Contains(string(body), "192.0.2.10"))
	}
}

// Acquiring the writer lock from Write checks that a slow network peer cannot
// hold up the importer after the HTML has been copied into memory.
type snapshotResponseWriter struct {
	*httptest.ResponseRecorder
	cfg     *config.Config
	log     *slog.Logger
	lockErr error
	checked bool
}

func (w *snapshotResponseWriter) Write(b []byte) (int, error) {
	if !w.checked {
		w.checked = true
		a, err := app.Open(w.cfg, w.log)
		if err != nil {
			w.lockErr = err
		} else {
			w.lockErr = a.Close()
		}
	}
	return w.ResponseRecorder.Write(b)
}
func TestHTMLSnapshotReleasesLockBeforeResponseWrite(t *testing.T) {
	e, s, ts, _ := serverFixture(t, "/postfix-report/")
	w := &snapshotResponseWriter{ResponseRecorder: httptest.NewRecorder(), cfg: e.Cfg, log: e.Log}
	s.ServeHTTP(w, httptest.NewRequest("GET", ts.URL+"/postfix-report/daily/2026-10-25.html", nil))
	if w.Code != 200 || !w.checked || w.lockErr != nil {
		t.Fatalf("code=%d checked=%v lock=%v", w.Code, w.checked, w.lockErr)
	}
}

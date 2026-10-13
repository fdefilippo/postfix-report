// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package report_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/norm"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/query"
	"github.com/fdefilippo/postfix-report/internal/report"
	"github.com/fdefilippo/postfix-report/internal/testutil"
	"github.com/fdefilippo/postfix-report/internal/verify"
)

var ctx = context.Background()

// fixture: 40 synthetic days from 2025-12-10 (crosses month and year), days
// 5 and 6 missing, a 3-hour hole on day 12, plus a hostile reject reason.
func fixture(t *testing.T) (*testutil.Env, *app.App, *report.Generator) {
	t.Helper()
	env := testutil.NewEnv(t)
	loc := env.Cfg.Location
	var buf bytes.Buffer
	err := testutil.Synthetic(&buf, testutil.SynthConfig{Start: time.Date(2025, 12, 10, 0, 0, 0, 0, loc), Days: 40, PerDay: 40,
		Missing: []int{5, 6}, GapDay: 12, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	buf.WriteString("Jan 18 12:00:00 mx1 postfix/smtpd[9]: NOQUEUE: reject: RCPT from unknown[203.0.113.66]: 554 5.7.1 <script>alert(1)</script> denied \"x\" & co; from=<a@example.com> to=<b@example.org> proto=ESMTP helo=<h>\n")
	testutil.WriteFile(t, env.LogPath("maillog"), buf.String(), time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC))
	a := env.Open(t)
	t.Cleanup(func() { a.Close() })
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	return env, a, newGen(t, env, a)
}

func newGen(t *testing.T, env *testutil.Env, a *app.App) *report.Generator {
	key, err := a.AnonKey()
	if err != nil {
		t.Fatal(err)
	}
	g := &report.Generator{DB: a.DB, Cfg: env.Cfg, Version: "test",
		Q:    &query.Q{DB: a.DB, Loc: env.Cfg.Location, Gap: int64(env.Cfg.Coverage.GapThreshold.Seconds())},
		Anon: norm.NewAnonymizer(key, env.Cfg.Privacy.AnonymizeEmail, env.Cfg.Privacy.AnonymizeIP)}
	if err := g.Init(); err != nil {
		t.Fatal(err)
	}
	return g
}

func read(t *testing.T, env *testutil.Env, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(env.Cfg.Output.Directory, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGenerateAllLinksAndOffline(t *testing.T) {
	env, a, g := fixture(t)
	res, err := g.GenerateAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 40 days + 7 weeks + 2 months + 2 years + index.
	pages := len(g.AllPages()) + 1
	if res.Rendered != pages {
		t.Errorf("rendered %d pages, want %d", res.Rendered, pages)
	}
	for _, p := range []string{"index.html", "daily/2025-12-10.html", "daily/2026-01-18.html", "weekly/2026-W01.html",
		"monthly/2025-12.html", "monthly/2026-01.html", "yearly/2025.html", "yearly/2026.html", "assets/css/report.css", "assets/js/report.js"} {
		if _, err := os.Stat(filepath.Join(env.Cfg.Output.Directory, p)); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	lr, err := verify.CheckLinks(env.Cfg.Output.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(lr.Broken) > 0 || len(lr.External) > 0 || len(lr.Temp) > 0 || len(lr.Foreign) > 0 {
		t.Errorf("links: broken %v, external %v, temp %v, foreign %v", lr.Broken, lr.External, lr.Temp, lr.Foreign)
	}
	if lr.Links < pages*10 {
		t.Errorf("suspiciously few links: %d", lr.Links)
	}
	// No absolute URLs, no fetch() of data, no CDN.
	for _, p := range []string{"index.html", "daily/2025-12-20.html", "assets/js/report.js"} {
		body := strings.ReplaceAll(read(t, env, p), "http://www.w3.org/2000/svg", "")
		for _, bad := range []string{"http://", "https://", "fetch(", "XMLHttpRequest"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q", p, bad)
			}
		}
	}
	r, err := verify.Run(a.DB, env.Cfg.Location, env.Cfg.Output.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if r.Failed() {
		for _, c := range r.Checks {
			if !c.OK {
				t.Errorf("verify %s: %s", c.Name, c.Detail)
			}
		}
	}
}

func TestMissingAndPartialPeriods(t *testing.T) {
	env, _, g := fixture(t)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	missing := read(t, env, "daily/2025-12-15.html") // day index 5
	if !strings.Contains(missing, "Dati assenti") || strings.Contains(missing, "Messaggi accettati") {
		t.Error("missing day must state that data is absent and show no KPI")
	}
	gap := read(t, env, "daily/2025-12-22.html") // day index 12, hole 13:00-16:00
	if !strings.Contains(gap, "Copertura parziale") {
		t.Error("day with a hole must be partial")
	}
	// The hourly chart of the hole has null values and status "missing".
	re := regexp.MustCompile(`"status":\[[^\]]*\]`)
	st := re.FindString(gap)
	if strings.Count(st, `"missing"`) != 3 {
		t.Errorf("hourly status: %s", st)
	}
	week := read(t, env, "weekly/2025-W51.html") // 15-21 Dec, two missing days
	if !strings.Contains(week, "2 senza dati") {
		t.Error("week coverage not reported")
	}
	if !strings.Contains(read(t, env, "monthly/2025-12.html"), "variazione non confrontabile") {
		t.Error("partial month must not show comparable variations")
	}
}

func TestEscaping(t *testing.T) {
	env, _, g := fixture(t)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	day := read(t, env, "daily/2026-01-18.html")
	if strings.Contains(day, "<script>alert(1)") {
		t.Fatal("unescaped HTML from log data")
	}
	if !strings.Contains(day, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("hostile reason not found escaped")
	}
}

func TestAnonymization(t *testing.T) {
	env, a, _ := fixture(t)
	env.Cfg.Privacy.AnonymizeIP = true
	env.Cfg.Privacy.AnonymizeEmail = true
	g := newGen(t, env, a)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	day := read(t, env, "daily/2026-01-12.html")
	if strings.Contains(day, "192.0.2.10<") || !strings.Contains(day, "192.0.2.0/24") {
		t.Error("IP not truncated")
	}
	if strings.Contains(day, ">app1<") || !strings.Contains(day, "u-") {
		t.Error("SASL user not pseudonymized")
	}
}

func modTimes(t *testing.T, dir string) map[string]time.Time {
	m := map[string]time.Time{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			m[p] = info.ModTime()
		}
		return nil
	})
	return m
}

func TestSelectiveRegeneration(t *testing.T) {
	env, a, g := fixture(t)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	before := modTimes(t, env.Cfg.Output.Directory)
	// Nothing dirty: nothing rewritten.
	res, err := g.GenerateDirty(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 0 {
		t.Errorf("clean run wrote %v", res.Paths)
	}
	// New lines for the last day (2026-01-18).
	f, _ := os.OpenFile(env.LogPath("maillog"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("Jan 18 23:59:00 mx1 postfix/smtpd[9]: NOQUEUE: reject: RCPT from unknown[203.0.113.67]: 554 5.7.1 <c@example.com>: Relay access denied; from=<a@example.com> to=<c@example.com> proto=ESMTP helo=<h>\n")
	f.Close()
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	g = newGen(t, env, a)
	res, err = g.GenerateDirty(ctx)
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]bool{}
	for _, p := range res.Paths {
		written[p] = true
	}
	for _, p := range []string{"daily/2026-01-18.html", "weekly/2026-W03.html", "monthly/2026-01.html", "yearly/2026.html", "index.html"} {
		if !written[p] {
			t.Errorf("%s not regenerated (written: %v)", p, res.Paths)
		}
	}
	for _, p := range []string{"daily/2025-12-10.html", "monthly/2025-12.html", "yearly/2025.html", "daily/2026-01-10.html"} {
		if written[p] {
			t.Errorf("%s regenerated without changes", p)
		}
	}
	if res.Rendered > 20 {
		t.Errorf("rendered %d pages for a one-day change", res.Rendered)
	}
	after := modTimes(t, env.Cfg.Output.Directory)
	untouched := filepath.Join(env.Cfg.Output.Directory, "daily", "2025-12-10.html")
	if !after[untouched].Equal(before[untouched]) {
		t.Error("untouched page file was rewritten")
	}
}

func TestGenerateOneOutOfRange(t *testing.T) {
	_, _, g := fixture(t)
	p, _ := period.Parse(period.Day, "2024-01-01", time.UTC)
	if _, err := g.GenerateOne(p); err == nil {
		t.Error("expected an error for a period without data")
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "page.html")
	for _, content := range []string{"one", "two"} {
		if err := report.WriteAtomic(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(p)
		if string(b) != content {
			t.Errorf("content %q", b)
		}
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
		t.Errorf("perm %v", st.Mode().Perm())
	}
}

// TestStaticWebServer crawls the site from index.html through a plain static
// file server under a URL prefix, following every relative link.
func TestStaticWebServer(t *testing.T) {
	env, _, g := fixture(t)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/some/prefix/", http.StripPrefix("/some/prefix/", http.FileServer(http.Dir(env.Cfg.Output.Directory))))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	start := srv.URL + "/some/prefix/index.html"
	seen := map[string]bool{start: true}
	queue := []string{start}
	re := regexp.MustCompile(`(?:href|src)="([^"#]+)"`)
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET %s: %d", u, resp.StatusCode)
			continue
		}
		if !strings.HasSuffix(u, ".html") {
			continue
		}
		base, _ := url.Parse(u)
		for _, m := range re.FindAllStringSubmatch(string(body), -1) {
			ref, _ := url.Parse(m[1])
			abs := base.ResolveReference(ref).String()
			if !strings.HasPrefix(abs, srv.URL+"/some/prefix/") {
				t.Errorf("%s links outside the prefix: %s", u, m[1])
				continue
			}
			if !seen[abs] {
				seen[abs] = true
				queue = append(queue, abs)
			}
		}
	}
	if want := len(g.AllPages()) + 3; len(seen) < want {
		t.Errorf("crawled %d resources, want at least %d", len(seen), want)
	}
}

// TestFileURLInBrowser opens a page through file:// in headless Chromium (if
// installed) and checks that the charts are rendered by the local script.
func TestFileURLInBrowser(t *testing.T) {
	bin := ""
	for _, b := range []string{"chromium", "chromium-browser", "google-chrome"} {
		if p, err := exec.LookPath(b); err == nil {
			bin = p
			break
		}
	}
	if bin == "" || testing.Short() {
		t.Skip("no headless browser available")
	}
	env, _, g := fixture(t)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"monthly/2026-01.html", "daily/2026-01-12.html", "index.html"} {
		u := "file://" + path.Join(filepath.ToSlash(env.Cfg.Output.Directory), p)
		cmd := exec.Command(bin, "--headless=new", "--no-sandbox", "--disable-gpu", "--user-data-dir="+t.TempDir(),
			"--virtual-time-budget=3000", "--dump-dom", u)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", bin, err)
		}
		dom := string(out)
		if !strings.Contains(dom, "<svg") || !strings.Contains(dom, "legend-item") || !strings.Contains(dom, `class="js"`) {
			t.Errorf("%s: charts not rendered via file:// (%d bytes)", p, len(dom))
		}
		if strings.Contains(dom, "Grafico non disponibile") {
			t.Errorf("%s: chart error", p)
		}
	}
}

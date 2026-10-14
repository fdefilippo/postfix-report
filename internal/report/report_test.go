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
	// No absolute URLs, no fetch() of data, no CDN. The project URL appears
	// only as plain text in the copyright footer, never as a link.
	const projectURL = "https://github.com/fdefilippo/postfix-report"
	for _, p := range []string{"index.html", "daily/2025-12-20.html", "assets/js/report.js"} {
		body := read(t, env, p)
		if strings.HasSuffix(p, ".html") {
			footer := "&lt;francesco@defilippo.org&gt; · " + projectURL + " · License: GNU GPLv3 or later</p>"
			if !strings.Contains(body, footer) {
				t.Errorf("%s: copyright footer missing", p)
			}
			body = strings.Replace(body, footer, "", 1)
		}
		body = strings.ReplaceAll(body, "http://www.w3.org/2000/svg", "")
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

// TestTabs checks that every page groups its sections into tabs whose
// buttons point to existing panels, with unique ids, and that the KPIs stay
// outside the tabs.
func TestTabs(t *testing.T) {
	env, _, g := fixture(t)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	common := []string{"Mittenti e destinatari", "Rifiuti ed errori", "Sicurezza", "Copertura"}
	want := map[string][]string{
		"daily/2026-01-12.html": append([]string{"Traffico", "Tempi di consegna"}, common...),
		"weekly/2026-W01.html":  append([]string{"Traffico", "Confronto", "Tempi di consegna"}, common...),
		"monthly/2026-01.html":  append([]string{"Traffico", "Confronto", "Tempi di consegna"}, common...),
		"yearly/2026.html":      append([]string{"Traffico", "Riepilogo mensile", "Confronto", "Tempi di consegna"}, common...),
		"index.html":            {"Ultimi sette giorni", "Archivio", "Report e acquisizioni", "Qualità dei dati"},
		"daily/2025-12-15.html": nil, // no data: coverage only, no tabs
	}
	tabRe := regexp.MustCompile(`<button type="button" role="tab" id="tab-([^"]+)" aria-controls="([^"]+)"[^>]*>([^<]+)</button>`)
	idRe := regexp.MustCompile(` id="([^"]+)"`)
	for p, labels := range want {
		body := read(t, env, p)
		tabs := tabRe.FindAllStringSubmatch(body, -1)
		var got []string
		for _, m := range tabs {
			got = append(got, m[3])
			if m[1] != m[2] {
				t.Errorf("%s: tab %q controls %q", p, m[1], m[2])
			}
			if !strings.Contains(body, `id="`+m[2]+`" role="tabpanel" aria-labelledby="tab-`+m[2]+`"`) {
				t.Errorf("%s: panel %q missing", p, m[2])
			}
		}
		if strings.Join(got, "|") != strings.Join(labels, "|") {
			t.Errorf("%s: tabs %q, want %q", p, got, labels)
		}
		seen := map[string]bool{}
		for _, m := range idRe.FindAllStringSubmatch(body, -1) {
			if seen[m[1]] {
				t.Errorf("%s: duplicate id %q", p, m[1])
			}
			seen[m[1]] = true
		}
		if labels != nil && strings.Index(body, `class="kpis"`) > strings.Index(body, `class="tabs"`) {
			t.Errorf("%s: KPIs inside the tabs", p)
		}
	}
}

// TestNav checks the top bar: the site title appears once, and every page
// lists every period kind, so that choosing a period never hides the others.
// Finer kinds point to the latest of them inside the page (its last day
// with data), coarser kinds to the periods containing it.
func TestNav(t *testing.T) {
	env, _, g := fixture(t)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"index.html":            "index.html Panoramica*|daily/2026-01-18.html Ultimo giorno|weekly/2026-W03.html Ultima settimana|monthly/2026-01.html Ultimo mese|yearly/2026.html Ultimo anno",
		"daily/2025-12-20.html": "../index.html Panoramica|../daily/2025-12-20.html Giorno*|../weekly/2025-W51.html Settimana|../monthly/2025-12.html Mese|../yearly/2025.html Anno",
		"weekly/2026-W01.html":  "../index.html Panoramica|../daily/2026-01-04.html Giorno|../weekly/2026-W01.html Settimana*|../monthly/2025-12.html Mese|../yearly/2025.html Anno",
		"monthly/2025-12.html":  "../index.html Panoramica|../daily/2025-12-31.html Giorno|../weekly/2026-W01.html Settimana|../monthly/2025-12.html Mese*|../yearly/2025.html Anno",
		"monthly/2026-01.html":  "../index.html Panoramica|../daily/2026-01-18.html Giorno|../weekly/2026-W03.html Settimana|../monthly/2026-01.html Mese*|../yearly/2026.html Anno",
		"yearly/2026.html":      "../index.html Panoramica|../daily/2026-01-18.html Giorno|../weekly/2026-W03.html Settimana|../monthly/2026-01.html Mese|../yearly/2026.html Anno*",
	}
	navRe := regexp.MustCompile(`(?s)<nav aria-label="Navigazione principale">(.*?)</nav>`)
	linkRe := regexp.MustCompile(`<a href="([^"]+)"( class="active")?[^>]*>([^<]+)</a>`)
	for p, w := range want {
		body := read(t, env, p)
		m := navRe.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%s: no top bar", p)
			continue
		}
		var got []string
		for _, l := range linkRe.FindAllStringSubmatch(m[1], -1) {
			s := l[1] + " " + l[3]
			if l[2] != "" {
				s += "*"
			}
			got = append(got, s)
		}
		if strings.Join(got, "|") != w {
			t.Errorf("%s: nav\n got %s\nwant %s", p, strings.Join(got, "|"), w)
		}
		if n := strings.Count(body, env.Cfg.Output.Title+"</a>"); n != 1 || strings.Contains(body, "<small>postfix-report</small>") {
			t.Errorf("%s: site title shown %d times in the top bar", p, n)
		}
	}
}

// TestWeekendShading checks that the charts over days mark Saturdays and
// Sundays, and that charts over hours or months do not.
func TestWeekendShading(t *testing.T) {
	env, _, g := fixture(t)
	if _, err := g.GenerateAll(ctx); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`"weekend":\[[^\]]*\]`)
	week := `"weekend":[false,false,false,false,false,true,true]`
	for p, want := range map[string]int{"weekly/2026-W01.html": 2, "daily/2026-01-10.html": 0, "yearly/2026.html": 0} {
		got := re.FindAllString(read(t, env, p), -1)
		if len(got) != want {
			t.Errorf("%s: %d charts with weekends, want %d", p, len(got), want)
		}
		for _, w := range got {
			if w != week {
				t.Errorf("%s: %s, want %s", p, w, week)
			}
		}
	}
	// December 2025 starts on a Monday: weekends on the 6th-7th, 13th-14th...
	m := re.FindString(read(t, env, "monthly/2025-12.html"))
	if !strings.HasPrefix(m, `"weekend":[false,false,false,false,false,true,true,false`) || strings.Count(m, "true") != 8 {
		t.Errorf("monthly/2025-12: %s", m)
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
	// A different layout (new version, templates or report options) renders
	// every page once, then nothing.
	g.Cfg.Reports.TopLimit = 5
	res, err = g.GenerateDirty(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(g.AllPages()) + 1; res.Rendered != want || res.Written == 0 {
		t.Errorf("layout change rendered %d pages, wrote %d; want %d rendered", res.Rendered, res.Written, want)
	}
	if res, err = g.GenerateDirty(ctx); err != nil || res.Rendered != 0 {
		t.Errorf("second run after layout change rendered %d pages (%v)", res.Rendered, err)
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
		// Tabs active: bar shown, one panel visible, the others hidden.
		if !strings.Contains(dom, `class="tabs ready"`) || strings.Count(dom, `aria-selected="true"`) != 1 {
			t.Errorf("%s: tabs not initialized", p)
		}
		if n, h := strings.Count(dom, `role="tabpanel"`), strings.Count(dom, `role="tabpanel" aria-labelledby="tab-`); n != h || n < 2 {
			t.Errorf("%s: %d panels", p, n)
		}
	}
	// The fragment selects a tab, and its charts get the real width.
	u := "file://" + path.Join(filepath.ToSlash(env.Cfg.Output.Directory), "daily/2026-01-12.html") + "#tempi-di-consegna"
	out, err := exec.Command(bin, "--headless=new", "--no-sandbox", "--disable-gpu", "--user-data-dir="+t.TempDir(),
		"--virtual-time-budget=3000", "--dump-dom", u).Output()
	if err != nil {
		t.Fatalf("%s: %v", bin, err)
	}
	if !strings.Contains(string(out), `id="tab-tempi-di-consegna" aria-controls="tempi-di-consegna" aria-selected="true"`) {
		t.Errorf("fragment #tempi-di-consegna did not select its tab")
	}
}

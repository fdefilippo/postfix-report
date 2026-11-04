// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/exporter"
	"github.com/fdefilippo/postfix-report/internal/norm"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/query"
	"github.com/fdefilippo/postfix-report/internal/report"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

type browser struct {
	t          *testing.T
	client     *http.Client
	base, csrf string
}

func newBrowser(t *testing.T, ts *httptest.Server, prefix string) *browser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, client: &http.Client{Jar: jar}, base: ts.URL + strings.TrimSuffix(prefix, "/")}
	code, data := b.call("GET", "/api/session", nil)
	if code != 200 {
		t.Fatalf("session %d %s", code, data)
	}
	var v map[string]string
	json.Unmarshal(data, &v)
	b.csrf = v["csrf"]
	return b
}
func (b *browser) call(method, rel string, data []byte) (int, []byte) {
	b.t.Helper()
	r, _ := http.NewRequest(method, b.base+rel, bytes.NewReader(data))
	if method == "POST" {
		r.Header.Set("Origin", strings.Split(b.base, "/")[0]+"//"+strings.Split(b.base, "/")[2])
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", b.csrf)
	}
	resp, err := b.client.Do(r)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp.StatusCode, out
}
func (b *browser) start(format string) (int, string) {
	code, data := b.call("POST", "/api/exports", []byte(`{"kind":"day","key":"2026-10-25","format":"`+format+`"}`))
	var v map[string]string
	json.Unmarshal(data, &v)
	return code, v["id"]
}
func (b *browser) ready(id string) {
	b.t.Helper()
	// Archive jobs render several periods. Match the service's two-minute
	// default budget, including under race instrumentation and CPU contention.
	deadline := time.Now().Add(2*time.Minute + 5*time.Second)
	for time.Now().Before(deadline) {
		code, data := b.call("GET", "/api/exports/"+id, nil)
		if code != 200 {
			b.t.Fatalf("job %d %s", code, data)
		}
		var v map[string]string
		json.Unmarshal(data, &v)
		if v["state"] == "ready" {
			return
		}
		if v["state"] == "failed" {
			b.t.Fatal("job failed", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
	b.t.Fatal("job timed out")
}

func serverFixture(t *testing.T, prefix string) (*testutil.Env, *Server, *httptest.Server, *atomic.Pointer[config.Config]) {
	t.Helper()
	e := testutil.NewEnv(t)
	e.Cfg.Web.Prefix = prefix
	e.Cfg.Web.QueueSize = 1
	l := testutil.NewLog(t)
	l.SMTPMessage("2026-10-25 10:00:00", "ABC01", "DEF02", "alice@example.org", 42, "bob@example.net")
	testutil.WriteFile(t, e.LogPath("maillog"), l.String(), time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	a := e.Open(t)
	if _, err := a.Import(context.Background(), "test", []string{e.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	key, err := a.AnonKey()
	if err != nil {
		t.Fatal(err)
	}
	g := &report.Generator{DB: a.DB, Q: &query.Q{DB: a.DB, Loc: e.Cfg.Location, Gap: 3600}, Cfg: e.Cfg, Anon: norm.NewAnonymizer(key, false, false), Version: "test"}
	if err = g.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err = g.GenerateAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Close()
	ptr := new(atomic.Pointer[config.Config])
	ptr.Store(e.Cfg)
	s, err := New(context.Background(), e.Cfg, func() (*config.Config, error) { return ptr.Load(), nil }, "test", e.Log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(func() { ts.Close(); s.Close() })
	return e, s, ts, ptr
}

func TestDownloadContainersSessionOwnershipAndPrefix(t *testing.T) {
	_, _, ts, _ := serverFixture(t, "/postfix-report/")
	b := newBrowser(t, ts, "/postfix-report/")
	other := newBrowser(t, ts, "/postfix-report/")
	code, html := b.call("GET", "/daily/2026-10-25.html", nil)
	if code != 200 || !bytes.Contains(html, []byte(`data-api="/postfix-report/api/"`)) {
		t.Fatalf("HTML %d %s", code, html)
	}
	for format, signature := range map[string]string{"pdf": "%PDF-", "odt": "PK"} {
		code, id := b.start(format)
		if code != 202 {
			t.Fatalf("start %d", code)
		}
		b.ready(id)
		if code, _ := other.call("GET", "/api/exports/"+id, nil); code != 404 {
			t.Fatal("another browser saw job", code)
		}
		if code, _ := other.call("GET", "/api/exports/"+id+"/download", nil); code != 404 {
			t.Fatal("another browser downloaded job", code)
		}
		code, data := b.call("GET", "/api/exports/"+id+"/download", nil)
		if code != 200 || !bytes.HasPrefix(data, []byte(signature)) {
			t.Fatalf("download %s %d", format, code)
		}
	}
}

func TestArchivePreviewValidationAndDownload(t *testing.T) {
	_, _, ts, _ := serverFixture(t, "/postfix-report/")
	b := newBrowser(t, ts, "/postfix-report/")
	other := newBrowser(t, ts, "/postfix-report/")
	code, html := b.call("GET", "/index.html", nil)
	if code != 200 || !bytes.Contains(html, []byte(`data-archive="true"`)) || !bytes.Contains(html, []byte("Esporta archivio completo")) {
		t.Fatal("archive UI absent from index", code)
	}
	for _, format := range []string{"pdf", "odt"} {
		body, _ := json.Marshal(request{Archive: &exporter.ArchiveSelection{}, Format: format})
		code, data := b.call("POST", "/api/archives/preview", body)
		var plan exporter.ArchivePlan
		if err := json.Unmarshal(data, &plan); err != nil || code != 200 || plan.Total != 4 || !plan.Allowed {
			t.Fatalf("preview %d %+v %s", code, plan, data)
		}
		code, data = b.call("POST", "/api/exports", body)
		var job map[string]string
		if err := json.Unmarshal(data, &job); err != nil || code != 202 {
			t.Fatal("archive start", code, string(data))
		}
		id := job["id"]
		b.ready(id)
		code, data = b.call("GET", "/api/exports/"+id+"/download", nil)
		signature := "%PDF-"
		if format == "odt" {
			signature = "PK"
		}
		if code != 200 || !bytes.HasPrefix(data, []byte(signature)) {
			t.Fatal("archive download", format, code)
		}
		if code, _ := other.call("GET", "/api/exports/"+id+"/download", nil); code != 404 {
			t.Fatal("archive exposed to another session")
		}
	}
	for _, body := range []string{
		`{"archive":{"kinds":[]},"format":"pdf"}`,
		`{"archive":{"kinds":["day","day"]},"format":"pdf"}`,
		`{"archive":{"from":"2026-10-25"},"format":"pdf"}`,
		`{"archive":{},"kind":"day","key":"2026-10-25","format":"pdf"}`,
		`{"archive":{"unknown":true},"format":"pdf"}`,
	} {
		for _, route := range []string{"/api/exports", "/api/archives/preview"} {
			if code, _ := b.call("POST", route, []byte(body)); code != 400 {
				t.Errorf("invalid archive accepted %d: %s", code, body)
			}
		}
	}
	b.csrf = "invalid"
	if code, _ := b.call("POST", "/api/archives/preview", []byte(`{"archive":{},"format":"pdf"}`)); code != 403 {
		t.Fatal("preview bypassed CSRF")
	}
}

func TestArchiveWorkerCancellation(t *testing.T) {
	_, s, ts, _ := serverFixture(t, "/")
	started := make(chan struct{})
	s.renderArchive = func(ctx context.Context, _ *config.Config, _ exporter.ArchiveSelection, _, _ string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	b := newBrowser(t, ts, "/")
	if code, _ := b.call("POST", "/api/exports", []byte(`{"archive":{},"format":"pdf"}`)); code != 202 {
		t.Fatal(code)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("archive job did not start")
	}
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("archive worker survived shutdown")
	}
}

func TestOriginCSRFAndRequestValidation(t *testing.T) {
	_, _, ts, _ := serverFixture(t, "/")
	b := newBrowser(t, ts, "/")
	for _, body := range []string{`{"kind":"year","key":"2026","format":"html"}`, `{"kind":"day","key":"../../etc/passwd","format":"pdf"}`, `{"kind":"day","key":"2026-10-25","format":"pdf","path":"/etc/passwd"}`, strings.Repeat("x", 2048)} {
		code, _ := b.call("POST", "/api/exports", []byte(body))
		if code != 400 {
			t.Errorf("body accepted %d", code)
		}
	}
	r, _ := http.NewRequest("POST", ts.URL+"/api/exports", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://evil.example.org")
	r.Header.Set("X-CSRF-Token", b.csrf)
	resp, err := b.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin accepted")
	}
	r, _ = http.NewRequest("GET", ts.URL+"/", nil)
	r.Host = "evil.example.org"
	resp, err = b.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("untrusted host accepted")
	}
	b.csrf = "wrong"
	if code, _ := b.start("pdf"); code != 403 {
		t.Fatal("invalid csrf", code)
	}
}

func TestBoundedQueueAndShutdownCancelsJobs(t *testing.T) {
	_, s, ts, _ := serverFixture(t, "/")
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.render = func(ctx context.Context, _ *config.Config, _ period.Period, _, _ string) ([]byte, error) {
		once.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []byte("%PDF-test"), nil
		}
	}
	b := newBrowser(t, ts, "/")
	if code, _ := b.start("pdf"); code != 202 {
		t.Fatal(code)
	}
	<-started
	if code, _ := b.start("pdf"); code != 202 {
		t.Fatal(code)
	}
	if code, _ := b.start("pdf"); code != 429 {
		t.Fatal("unbounded queue", code)
	}
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker survived cancellation")
	}
	close(release)
}

func TestPrivacyChangeInvalidatesResultsAndOldHTML(t *testing.T) {
	e, _, ts, ptr := serverFixture(t, "/")
	b := newBrowser(t, ts, "/")
	code, id := b.start("odt")
	if code != 202 {
		t.Fatal(code)
	}
	b.ready(id)
	next := *e.Cfg
	next.Privacy.AnonymizeEmail = true
	ptr.Store(&next)
	if code, _ := b.call("GET", "/api/exports/"+id+"/download", nil); code != 410 {
		t.Fatal("old privacy result downloaded", code)
	}
	if code, _ := b.call("GET", "/daily/2026-10-25.html", nil); code != 503 {
		t.Fatal("old privacy HTML exposed", code)
	}
}

func TestStaticFilesCannotEscapeOrExposeDatabase(t *testing.T) {
	e, _, ts, _ := serverFixture(t, "/")
	secret := filepath.Join(e.Dir, "secret")
	os.WriteFile(secret, []byte("private"), 0600)
	if err := os.Symlink(secret, filepath.Join(e.Cfg.Output.Directory, "daily", "2026-10-26.html")); err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t, ts, "/")
	for _, p := range []string{"/report.db", "/.secret", "/assets/", "/daily/2026-10-26.html", "/api/exports/../../secret"} {
		code, data := b.call("GET", p, nil)
		if code == 200 || bytes.Contains(data, []byte("private")) {
			t.Fatalf("exposed %s: %d %s", p, code, data)
		}
	}
}

// Exercise the actual browser script under the production CSP, including cookie,
// CSRF, asynchronous completion and its automatic attachment request.
func TestBrowserExport(t *testing.T) {
	for _, path := range []string{"/daily/2026-10-25.html", "/index.html"} {
		t.Run(path, func(t *testing.T) { testBrowserExport(t, path) })
	}
}

func testBrowserExport(t *testing.T, pagePath string) {
	if testing.Short() {
		t.Skip("headless browser skipped in short mode")
	}
	chrome, err := exec.LookPath("chromium")
	if err != nil {
		t.Skip("chromium not installed")
	}
	_, s, _, _ := serverFixture(t, "/")
	var downloaded atomic.Bool
	finished := make(chan struct{})
	var finishOnce sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("browser request %s %s", r.Method, r.URL.Path)
		if r.URL.Path == "/browser-done" {
			if !downloaded.Load() {
				w.WriteHeader(http.StatusConflict)
				return
			}
			finishOnce.Do(func() { close(finished) })
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/browser-test.js" {
			w.Header().Set("Content-Type", "application/javascript")
			io.WriteString(w, `function begin(){
var button = document.querySelector('[data-export="pdf"]');
if(!button || button.disabled){setTimeout(begin,50);return;}
button.click();
fetch('/browser-wait').then(function(r){ if(!r.ok) throw new Error('job failed');
function complete(){
 if(!document.querySelector('#document-export [role=status] a')) { setTimeout(complete,100); return; }
 document.body.setAttribute('data-export-test','ready');
 fetch('/browser-done').then(function(r){if(!r.ok) setTimeout(complete,100);});
}
complete(); }); } begin();`)
			return
		}
		if r.URL.Path == "/browser-wait" {
			deadline := time.NewTimer(20 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				s.mu.Lock()
				state := ""
				for _, j := range s.jobs {
					state = j.State
				}
				s.mu.Unlock()
				if state == "ready" {
					w.WriteHeader(200)
					return
				}
				if state == "failed" {
					http.Error(w, "job failed", 500)
					return
				}
				select {
				case <-r.Context().Done():
					return
				case <-deadline.C:
					http.Error(w, "timeout", http.StatusGatewayTimeout)
					return
				case <-tick.C:
				}
			}
		}
		if strings.HasSuffix(r.URL.Path, ".html") {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, r)
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			w.Write(bytes.Replace(rec.Body.Bytes(), []byte("</body>"), []byte(`<script src="/browser-test.js" defer></script></body>`), 1))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/download") {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, r)
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			n, err := w.Write(rec.Body.Bytes())
			if rec.Code == 200 && err == nil && n == rec.Body.Len() && bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF-")) {
				downloaded.Store(true)
			}
			return
		}
		s.ServeHTTP(w, r)
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Attachment downloads can keep headless Chromium alive after dumping
	// the DOM. Stop it explicitly only after the browser confirms the ready
	// link and the server has written a successful PDF attachment response.
	go func() {
		select {
		case <-finished:
			cancel()
		case <-ctx.Done():
		}
	}()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-proxy-server", "--disable-background-networking", "--disable-dev-shm-usage", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=5000", "--dump-dom", ts.URL+pagePath)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	select {
	case <-finished:
		// Cancellation is expected after both independent success signals.
		if !downloaded.Load() {
			t.Fatal("browser completion without PDF response")
		}
	default:
		t.Fatalf("browser export did not complete: %v (download=%v) %s", err, downloaded.Load(), out)
	}
}

func TestPolicyChangeDuringRenderDiscardsResult(t *testing.T) {
	e, s, ts, ptr := serverFixture(t, "/")
	started, release := make(chan struct{}), make(chan struct{})
	s.render = func(ctx context.Context, _ *config.Config, _ period.Period, _, _ string) ([]byte, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []byte("%PDF-private-old-policy"), nil
		}
	}
	b := newBrowser(t, ts, "/")
	code, id := b.start("pdf")
	if code != 202 {
		t.Fatal(code)
	}
	<-started
	next := *e.Cfg
	next.Privacy.AnonymizeEmail = true
	ptr.Store(&next)
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	finished := false
	for time.Now().Before(deadline) {
		s.mu.Lock()
		state := s.jobs[id].State
		s.mu.Unlock()
		if state == "failed" {
			finished = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !finished {
		t.Fatal("worker retained stale result after policy change")
	}
	if code, data := b.call("GET", "/api/exports/"+id+"/download", nil); code != 410 || bytes.Contains(data, []byte("private-old-policy")) {
		t.Fatalf("stale result exposed %d %s", code, data)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stored != 0 {
		t.Fatal("stale document retained")
	}
}

func TestExpiredDownloadReleasesResult(t *testing.T) {
	_, s, ts, _ := serverFixture(t, "/")
	s.render = func(context.Context, *config.Config, period.Period, string, string) ([]byte, error) {
		return []byte("%PDF-expiring"), nil
	}
	b := newBrowser(t, ts, "/")
	code, id := b.start("pdf")
	if code != 202 {
		t.Fatal(code)
	}
	b.ready(id)
	s.mu.Lock()
	s.jobs[id].created = time.Now().Add(-s.base.Web.JobTTL.Duration - time.Second)
	s.mu.Unlock()
	if code, _ := b.call("GET", "/api/exports/"+id+"/download", nil); code != 410 {
		t.Fatal("expired result available", code)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stored != 0 || len(s.jobs) != 0 {
		t.Fatal("expired result retained")
	}
}

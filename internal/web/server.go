// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package web serves existing dashboards and bounded, session-owned export jobs.
// Network authentication and TLS are provided by a reverse proxy; the listener
// accepts only numeric loopback addresses.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/exporter"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
)

//go:embed assets/export.js
var assets embed.FS

const maxStoredBytes = 64 << 20
const maxJobs = 32

// LoadConfig returns a fresh, validated immutable configuration. Reading it for
// each job/download makes privacy changes fail closed without restarting serve.
type LoadConfig func() (*config.Config, error)

type request struct {
	Kind    period.Kind                `json:"kind"`
	Key     string                     `json:"key"`
	Format  string                     `json:"format"`
	Archive *exporter.ArchiveSelection `json:"archive,omitempty"`
}
type job struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
	owner, policy string
	req           request
	cfg           *config.Config
	created       time.Time
	data          []byte
}

// Server owns workers and results. Call Close before discarding it.
type Server struct {
	base          *config.Config
	load          LoadConfig
	version       string
	log           *slog.Logger
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	queue         chan *job
	mu            sync.Mutex
	jobs          map[string]*job
	stored        int
	secret        [32]byte
	root          *os.Root
	render        func(context.Context, *config.Config, period.Period, string, string) ([]byte, error)
	renderArchive func(context.Context, *config.Config, exporter.ArchiveSelection, string, string) ([]byte, error)
}

// New starts a finite worker pool. It never migrates or creates the database.
func New(ctx context.Context, cfg *config.Config, load LoadConfig, version string, log *slog.Logger) (*Server, error) {
	if err := cfg.Finish(); err != nil {
		return nil, err
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := store.OpenReader(probe, cfg.Database.Path)
	if err != nil {
		return nil, err
	}
	db.Close()
	root, err := os.OpenRoot(cfg.Output.Directory)
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(ctx)
	s := &Server{base: cfg, load: load, version: version, log: log, ctx: ctx, cancel: stop, queue: make(chan *job, cfg.Web.QueueSize), jobs: make(map[string]*job), root: root, render: exporter.Render, renderArchive: exporter.RenderArchive}
	if _, err = rand.Read(s.secret[:]); err != nil {
		root.Close()
		stop()
		return nil, err
	}
	for i := 0; i < cfg.Web.Workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	s.wg.Add(1)
	go s.reaper()
	return s, nil
}

// Close cancels jobs and waits for workers; all in-memory documents are released.
func (s *Server) Close() error {
	s.cancel()
	s.wg.Wait()
	s.mu.Lock()
	clear(s.jobs)
	s.stored = 0
	s.mu.Unlock()
	return s.root.Close()
}

// Run listens in foreground until cancellation and shuts HTTP down gracefully.
func Run(ctx context.Context, cfg *config.Config, load LoadConfig, version string, log *slog.Logger) error {
	s, err := New(ctx, cfg, load, version, log)
	if err != nil {
		return err
	}
	defer s.Close()
	ln, err := net.Listen("tcp", cfg.Web.Listen)
	if err != nil {
		return err
	}
	h := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- h.Serve(ln) }()
	log.Info("web listening", "address", ln.Addr(), "prefix", cfg.Web.Prefix)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		s.cancel()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := h.Shutdown(shutdown)
		if err != nil {
			_ = h.Close()
		}
		<-done
		return err
	}
}

func (s *Server) current() (*config.Config, error) {
	c, err := s.load()
	if err != nil {
		return nil, err
	}
	if c.Database.Path != s.base.Database.Path || c.Output.Directory != s.base.Output.Directory || c.Web != s.base.Web {
		return nil, errors.New("web endpoints or directories changed: restart serve")
	}
	return c, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
	if !s.validHost(r) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(r.URL.Path, s.base.Web.Prefix) {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, s.base.Web.Prefix)
	if path.Clean("/"+rel) != "/"+rel && rel != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rel == "api/export.js" && r.Method == http.MethodGet {
		b, _ := assets.ReadFile("assets/export.js")
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(b)
		return
	}
	if rel == "api/session" && r.Method == http.MethodGet {
		s.session(w, r)
		return
	}
	if rel == "api/archives/preview" && r.Method == http.MethodPost {
		s.previewArchive(w, r)
		return
	}
	if rel == "api/exports" && r.Method == http.MethodPost {
		s.create(w, r)
		return
	}
	if strings.HasPrefix(rel, "api/exports/") && r.Method == http.MethodGet {
		s.result(w, r, strings.TrimPrefix(rel, "api/exports/"))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.static(w, r, rel)
}

func (s *Server) validHost(r *http.Request) bool {
	if s.base.Web.PublicOrigin != "" {
		u, _ := url.Parse(s.base.Web.PublicOrigin)
		return strings.EqualFold(r.Host, u.Host)
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (s *Server) origin(r *http.Request) string {
	if s.base.Web.PublicOrigin != "" {
		return s.base.Web.PublicOrigin
	}
	return "http://" + r.Host
}
func (s *Server) mac(v string) string {
	h := hmac.New(sha256.New, s.secret[:])
	h.Write([]byte(v))
	return hex.EncodeToString(h.Sum(nil))
}
func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (s *Server) cookie(r *http.Request) string {
	c, err := r.Cookie("postfix_report_session")
	if err != nil {
		return ""
	}
	p := strings.Split(c.Value, ".")
	if len(p) != 3 {
		return ""
	}
	n, err := strconv.ParseInt(p[1], 10, 64)
	if err != nil || time.Since(time.Unix(n, 0)) < 0 || time.Since(time.Unix(n, 0)) > time.Hour {
		return ""
	}
	if !hmac.Equal([]byte(s.mac(p[0]+"."+p[1])), []byte(p[2])) {
		return ""
	}
	return c.Value
}
func (s *Server) owner(r *http.Request, cookie string) string {
	user, _, _ := r.BasicAuth()
	return s.mac(cookie + "\x00" + user)
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); o != "" && o != s.origin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	c := s.cookie(r)
	if c == "" {
		v := randomID() + "." + strconv.FormatInt(time.Now().Unix(), 10)
		c = v + "." + s.mac(v)
		http.SetCookie(w, &http.Cookie{Name: "postfix_report_session", Value: c, Path: s.base.Web.Prefix, HttpOnly: true, Secure: strings.HasPrefix(s.origin(r), "https://"), SameSite: http.SameSiteStrictMode, MaxAge: 3600})
	}
	writeJSON(w, 200, map[string]string{"csrf": s.mac("csrf:" + c)})
}

func (s *Server) decodeExportRequest(w http.ResponseWriter, r *http.Request) (*config.Config, *request) {
	cookie := s.cookie(r)
	if cookie == "" || r.Header.Get("Origin") != s.origin(r) || !hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.mac("csrf:"+cookie))) {
		http.Error(w, "invalid export session or CSRF token", http.StatusForbidden)
		return nil, nil
	}
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
		return nil, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req request
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid export request", http.StatusBadRequest)
		return nil, nil
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		http.Error(w, "invalid export request", http.StatusBadRequest)
		return nil, nil
	}
	cfg, err := s.current()
	if err != nil {
		http.Error(w, "configuration changed or unavailable", http.StatusServiceUnavailable)
		return nil, nil
	}
	valid := req.Format == "pdf" || req.Format == "odt"
	if req.Archive != nil {
		valid = valid && req.Kind == "" && req.Key == "" && req.Archive.Validate(cfg) == nil
	} else {
		_, err = period.Parse(req.Kind, req.Key, cfg.Location)
		valid = valid && err == nil
	}
	if !valid {
		http.Error(w, "invalid period or format", http.StatusBadRequest)
		return nil, nil
	}
	return cfg, &req
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	cfg, req := s.decodeExportRequest(w, r)
	if req == nil {
		return
	}
	cookie := s.cookie(r)
	j := &job{ID: randomID(), State: "queued", owner: s.owner(r, cookie), policy: exporter.PolicyKey(cfg), req: *req, cfg: cfg, created: time.Now()}
	s.mu.Lock()
	if len(s.jobs) >= maxJobs {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "10")
		http.Error(w, "export capacity reached", http.StatusTooManyRequests)
		return
	}
	s.jobs[j.ID] = j
	select {
	case s.queue <- j:
		s.mu.Unlock()
		writeJSON(w, 202, map[string]string{"id": j.ID, "state": "queued"})
	default:
		delete(s.jobs, j.ID)
		s.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		http.Error(w, "export queue is full", http.StatusTooManyRequests)
	}
}

func (s *Server) result(w http.ResponseWriter, r *http.Request, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) > 2 || len(parts[0]) != 48 || (len(parts) == 2 && parts[1] != "download") {
		http.NotFound(w, r)
		return
	}
	cookie := s.cookie(r)
	cfg, err := s.current()
	if cookie == "" || err != nil {
		http.Error(w, "export session or configuration expired", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	j := s.jobs[parts[0]]
	if j == nil || j.owner != s.owner(r, cookie) {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if j.policy != exporter.PolicyKey(cfg) || time.Since(j.created) > s.base.Web.JobTTL.Duration {
		s.stored -= len(j.data)
		delete(s.jobs, j.ID)
		s.mu.Unlock()
		http.Error(w, "export expired: request it again", http.StatusGone)
		return
	}
	if len(parts) == 1 {
		v := map[string]string{"id": j.ID, "state": j.State}
		if j.Error != "" {
			v["error"] = j.Error
		}
		s.mu.Unlock()
		writeJSON(w, 200, v)
		return
	}
	if j.State != "ready" {
		s.mu.Unlock()
		http.Error(w, "export is not ready", http.StatusConflict)
		return
	}
	data, format, name := j.data, j.req.Format, string(j.req.Kind)+"-"+j.req.Key+"."+j.req.Format
	if j.req.Archive != nil {
		name = "archive-all." + format
		if j.req.Archive.From != "" {
			name = "archive-" + j.req.Archive.From + "-" + j.req.Archive.To + "." + format
		}
	}
	s.mu.Unlock()
	ctype := "application/pdf"
	if format == "odt" {
		ctype = "application/vnd.oasis.opendocument.text"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

func (s *Server) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.queue:
			if s.ctx.Err() != nil {
				return
			}
			s.mu.Lock()
			if _, ok := s.jobs[j.ID]; !ok || time.Since(j.created) > s.base.Web.JobTTL.Duration {
				delete(s.jobs, j.ID)
				s.mu.Unlock()
				continue
			}
			j.State = "running"
			s.mu.Unlock()
			ctx, cancel := context.WithTimeout(s.ctx, s.base.Web.JobTimeout.Duration)
			var b []byte
			var err error
			if j.req.Archive != nil {
				b, err = s.renderArchive(ctx, j.cfg, *j.req.Archive, j.req.Format, s.version)
			} else {
				var p period.Period
				p, err = period.Parse(j.req.Kind, j.req.Key, j.cfg.Location)
				if err == nil {
					b, err = s.render(ctx, j.cfg, p, j.req.Format, s.version)
				}
			}
			if err == nil {
				err = ctx.Err()
			}
			cancel()
			cfg, loadErr := s.current()
			if loadErr != nil || (cfg != nil && exporter.PolicyKey(cfg) != j.policy) {
				err = errors.New("configuration changed; request the export again")
			}
			s.mu.Lock()
			if _, exists := s.jobs[j.ID]; !exists {
				s.mu.Unlock()
				continue
			}
			if err == nil && (len(b) > 32<<20 || s.stored+len(b) > maxStoredBytes) {
				err = errors.New("export memory capacity reached; retry later")
			}
			if err != nil {
				j.State = "failed"
				j.Error = "Esportazione non riuscita. Riprovare dopo l’aggiornamento o consultare il log. / Export failed. Retry after updating or check the log."
				if j.req.Archive != nil {
					j.Error = "Archivio non esportabile: verificare i dati o ridurre intervallo e tipi di report (massimo 200 report, 2000 pagine, 32 MiB). Consultare il log. / Archive export failed: check data or select fewer periods (maximum 200 reports, 2000 pages, 32 MiB). Check the log."
				}
				s.log.Warn("export failed", "kind", j.req.Kind, "period", j.req.Key, "error", err)
			} else {
				j.State = "ready"
				j.data = b
				s.stored += len(b)
			}
			s.mu.Unlock()
		}
	}
}

func (s *Server) reaper() {
	defer s.wg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			for id, j := range s.jobs {
				if time.Since(j.created) > s.base.Web.JobTTL.Duration {
					s.stored -= len(j.data)
					delete(s.jobs, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) static(w http.ResponseWriter, r *http.Request, rel string) {
	if rel == "" {
		rel = "index.html"
	}
	var selected *period.Period
	allowed := rel == "index.html" || rel == "assets/js/report.js" || rel == "assets/css/report.css"
	for _, k := range period.Kinds {
		prefix := map[period.Kind]string{period.Day: "daily/", period.Week: "weekly/", period.Month: "monthly/", period.Year: "yearly/"}[k]
		if strings.HasPrefix(rel, prefix) && strings.HasSuffix(rel, ".html") {
			p, err := period.Parse(k, strings.TrimSuffix(strings.TrimPrefix(rel, prefix), ".html"), s.base.Location)
			if err == nil && p.Path() == rel {
				selected = &p
				allowed = true
			}
			break
		}
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	cfg, err := s.current()
	if err != nil {
		http.Error(w, "configuration changed: restart serve", http.StatusServiceUnavailable)
		return
	}
	if strings.HasSuffix(rel, ".html") {
		// Reconciliation writes its privacy marker only after rewriting every
		// managed page. Until then old, less restricted HTML is not served.
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		unlock, e := store.ReadLock(ctx, cfg.Database.Path)
		if e != nil {
			http.Error(w, "report update in progress; retry shortly", http.StatusServiceUnavailable)
			return
		}
		db, e := store.OpenReader(ctx, cfg.Database.Path)
		if e != nil {
			unlock()
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		var marker string
		e = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='report_privacy'`).Scan(&marker)
		db.Close()
		unlock()
		want := fmt.Sprintf("%t/%t/%s", cfg.Privacy.AnonymizeEmail, cfg.Privacy.AnonymizeIP, cfg.Output.Directory)
		if e != nil || marker != want {
			http.Error(w, "privacy settings changed: run update before serving reports", http.StatusServiceUnavailable)
			return
		}
	}
	f, err := s.root.Open(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if !strings.HasSuffix(rel, ".html") {
		http.ServeContent(w, r, rel, st.ModTime(), f)
		return
	}
	if st.Size() > 16<<20 {
		http.Error(w, "report exceeds HTML size limit", http.StatusRequestEntityTooLarge)
		return
	}
	b, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(b) > 16<<20 {
		http.Error(w, "cannot read report", http.StatusInternalServerError)
		return
	}
	if selected != nil {
		label := "Esporta il report completo"
		if cfg.Output.Language == "en" {
			label = "Export the complete report"
		}
		fragment := fmt.Sprintf(`<aside class="card noprint" id="document-export" data-kind="%s" data-key="%s" data-api="%sapi/" data-lang="%s"><strong>%s</strong> <button type="button" data-export="pdf">PDF</button> <button type="button" data-export="odt">ODT</button><span role="status" aria-live="polite"></span></aside><script src="%sapi/export.js" defer></script>`, selected.Kind, selected.Key, cfg.Web.Prefix, cfg.Output.Language, label, cfg.Web.Prefix)
		b = []byte(strings.Replace(string(b), "</main>", fragment+"</main>", 1))
	}
	if rel == "index.html" {
		b = []byte(strings.Replace(string(b), "<main>", "<main>"+archivePanel(cfg), 1))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodHead {
		w.Write(b)
	}
}

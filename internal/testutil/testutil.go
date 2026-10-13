// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package testutil builds synthetic Postfix logs and test environments.
// All data is fictitious (example.org/.net/.com, RFC 5737 addresses).
package testutil

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/config"
)

// Log accumulates syslog lines.
type Log struct {
	Host  string
	Loc   *time.Location
	lines []string
	pid   int
}

// NewLog creates a builder for host mx1 in Europe/Rome.
func NewLog(t testing.TB) *Log {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	return &Log{Host: "mx1", Loc: loc, pid: 1000}
}

// Add appends a line "<ts> host postfix/<proc>[pid]: <msg>"; ts is
// "2006-01-02 15:04:05" local time.
func (l *Log) Add(ts, proc, msg string) *Log {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, l.Loc)
	if err != nil {
		panic(err)
	}
	l.pid++
	l.lines = append(l.lines, fmt.Sprintf("%s %s postfix/%s[%d]: %s", t.Format(time.Stamp), l.Host, proc, l.pid, msg))
	return l
}

// Raw appends a raw line.
func (l *Log) Raw(line string) *Log { l.lines = append(l.lines, line); return l }

// Lines returns the lines.
func (l *Log) Lines() []string { return append([]string(nil), l.lines...) }

// String returns the log content with trailing newline.
func (l *Log) String() string { return strings.Join(l.lines, "\n") + "\n" }

// SMTPMessage appends the lines of a message received via SMTP, passed
// through the content filter (relay=filter, reinjected with uid 97) and
// delivered to each recipient via smtp. ts is the acceptance time; qid and
// rqid are the original and reinjected queue IDs.
func (l *Log) SMTPMessage(ts, qid, rqid, from string, size int, rcpts ...string) *Log {
	t, _ := time.ParseInLocation("2006-01-02 15:04:05", ts, l.Loc)
	at := func(sec int) string { return t.Add(time.Duration(sec) * time.Second).Format("2006-01-02 15:04:05") }
	mid := qid + "@app.example.net"
	l.Add(at(0), "smtpd", "connect from app.example.net[192.0.2.10]")
	l.Add(at(0), "smtpd", qid+": client=app.example.net[192.0.2.10]")
	l.Add(at(0), "cleanup", qid+": message-id=<"+mid+">")
	l.Add(at(0), "qmgr", fmt.Sprintf("%s: from=<%s>, size=%d, nrcpt=%d (queue active)", qid, from, size, len(rcpts)))
	l.Add(at(0), "smtpd", "disconnect from app.example.net[192.0.2.10] ehlo=1 starttls=1 mail=1 rcpt=1 data=1 quit=1 commands=6")
	l.Add(at(1), "pickup", fmt.Sprintf("%s: uid=97 from=<%s>", rqid, from))
	l.Add(at(1), "cleanup", rqid+": message-id=<"+mid+">")
	l.Add(at(1), "qmgr", fmt.Sprintf("%s: from=<%s>, size=%d, nrcpt=%d (queue active)", rqid, from, size+90, len(rcpts)))
	for _, r := range rcpts {
		l.Add(at(1), "pipe", fmt.Sprintf("%s: to=<%s>, relay=filter, delay=0.3, delays=0.2/0/0/0.1, dsn=2.0.0, status=sent (delivered via filter service)", qid, r))
	}
	l.Add(at(1), "qmgr", qid+": removed")
	for _, r := range rcpts {
		l.Add(at(2), "smtp", fmt.Sprintf("%s: to=<%s>, relay=mx.example.com[198.51.100.20]:25, delay=1.5, delays=0.1/0/0.4/1, dsn=2.0.0, status=sent (250 2.0.0 Ok: queued)", rqid, r))
	}
	l.Add(at(2), "qmgr", rqid+": removed")
	return l
}

// WriteFile writes content to path (gzip-compressed if path ends in .gz)
// and sets its mtime.
func WriteFile(t testing.TB, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var w io.Writer = f
	var gz *gzip.Writer
	if strings.HasSuffix(path, ".gz") {
		gz = gzip.NewWriter(f)
		w = gz
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatal(err)
	}
	if gz != nil {
		gz.Close()
	}
	f.Close()
	if !mtime.IsZero() {
		os.Chtimes(path, mtime, mtime)
	}
}

// Env is an isolated environment: config, database and output directory.
type Env struct {
	Dir string
	Cfg *config.Config
	Log *slog.Logger
}

// NewEnv creates an environment in a temporary directory.
func NewEnv(t testing.TB) *Env {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "db", "report.db")
	cfg.Output.Directory = filepath.Join(dir, "www")
	cfg.Input.Paths = []string{filepath.Join(dir, "log", "maillog*")}
	cfg.Logging.Level = "error"
	if err := cfg.Finish(); err != nil {
		t.Fatal(err)
	}
	return &Env{Dir: dir, Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// Open opens the app (the caller must Close it).
func (e *Env) Open(t testing.TB) *app.App {
	t.Helper()
	a, err := app.Open(e.Cfg, e.Log)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// LogPath returns a path in the environment log directory.
func (e *Env) LogPath(name string) string { return filepath.Join(e.Dir, "log", name) }

// RealLogPath returns the path of the reference maillog used by regression
// tests ("" if absent). It is never distributed: tests skip without it.
// POSTFIX_REPORT_REAL_LOG overrides the default locations
// (samples/maillog, then maillog at the repository root).
func RealLogPath() string {
	if p := os.Getenv("POSTFIX_REPORT_REAL_LOG"); p != "" {
		return p
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	for _, p := range []string{filepath.Join(root, "samples", "maillog"), filepath.Join(root, "maillog")} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

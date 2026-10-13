// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package app_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	ag "github.com/fdefilippo/postfix-report/internal/aggregate"
	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/query"
	"github.com/fdefilippo/postfix-report/internal/testutil"
	"github.com/fdefilippo/postfix-report/internal/verify"
)

var ctx = context.Background()

// scenario builds a synthetic log covering the lifecycles of LOG_ANALYSIS.md.
func scenario(t *testing.T) *testutil.Log {
	l := testutil.NewLog(t)
	// Coverage anchors: the day is observed from 00:00:00 to 23:59:59.
	l.Add("2026-10-12 00:00:00", "anvil", "statistics: max cache size 1 at Oct 12 00:00:00")
	for h := 1; h < 24; h++ {
		l.Add(time.Date(2026, 10, 12, h, 0, 0, 0, l.Loc).Format("2006-01-02 15:04:05"), "anvil", "statistics: max cache size 1 at x")
	}
	// 1. SMTP message with two recipients through the content filter.
	l.SMTPMessage("2026-10-12 10:00:00", "A000000001", "B000000001", "alice@example.org", 1000, "x@example.com", "y@example.com")
	// 2. Deferred, then delivered.
	l.Add("2026-10-12 10:05:00", "smtpd", "C000000002: client=app.example.net[192.0.2.10], sasl_method=LOGIN, sasl_username=app1")
	l.Add("2026-10-12 10:05:00", "cleanup", "C000000002: message-id=<c2@example.net>")
	l.Add("2026-10-12 10:05:00", "qmgr", "C000000002: from=<bob@example.org>, size=2000, nrcpt=1 (queue active)")
	l.Add("2026-10-12 10:05:01", "pickup", "D000000002: uid=97 from=<bob@example.org>")
	l.Add("2026-10-12 10:05:01", "cleanup", "D000000002: message-id=<c2@example.net>")
	l.Add("2026-10-12 10:05:01", "qmgr", "D000000002: from=<bob@example.org>, size=2090, nrcpt=1 (queue active)")
	l.Add("2026-10-12 10:05:01", "pipe", "C000000002: to=<z@example.net>, relay=filter, delay=0.4, delays=0.3/0/0/0.1, dsn=2.0.0, status=sent (delivered via filter service)")
	l.Add("2026-10-12 10:05:01", "qmgr", "C000000002: removed")
	l.Add("2026-10-12 10:05:31", "smtp", "D000000002: to=<z@example.net>, relay=none, delay=30, delays=0/0/30/0, dsn=4.4.1, status=deferred (connect to mx.example.net[203.0.113.7]:25: Connection timed out)")
	l.Add("2026-10-12 10:15:00", "qmgr", "D000000002: from=<bob@example.org>, size=2090, nrcpt=1 (queue active)")
	l.Add("2026-10-12 10:15:01", "smtp", "D000000002: to=<z@example.net>, relay=mx.example.net[203.0.113.7]:25, delay=600, delays=599/0/0.5/0.5, dsn=2.0.0, status=sent (250 ok)")
	l.Add("2026-10-12 10:15:01", "qmgr", "D000000002: removed")
	// 3. Local message, bounced, with its notification delivered.
	l.Add("2026-10-12 11:00:00", "pickup", "E000000003: uid=0 from=<root>")
	l.Add("2026-10-12 11:00:00", "cleanup", "E000000003: message-id=<e3@mx1.example.org>")
	l.Add("2026-10-12 11:00:00", "qmgr", "E000000003: from=<root@mx1.example.org>, size=500, nrcpt=1 (queue active)")
	l.Add("2026-10-12 11:00:01", "smtp", "E000000003: to=<nobody@example.com>, relay=mx.example.com[198.51.100.20]:25, delay=1, delays=0/0/0.5/0.5, dsn=5.1.1, status=bounced (host mx.example.com[198.51.100.20] said: 550 5.1.1 <nobody@example.com>: User unknown (in reply to RCPT TO command))")
	l.Add("2026-10-12 11:00:01", "cleanup", "F000000003: message-id=<f3@mx1.example.org>")
	l.Add("2026-10-12 11:00:01", "bounce", "E000000003: sender non-delivery notification: F000000003")
	l.Add("2026-10-12 11:00:01", "qmgr", "F000000003: from=<>, size=2500, nrcpt=1 (queue active)")
	l.Add("2026-10-12 11:00:01", "qmgr", "E000000003: removed")
	l.Add("2026-10-12 11:00:02", "smtp", "F000000003: to=<root@mx1.example.org>, relay=mx.example.org[192.0.2.5]:25, delay=0.5, delays=0/0/0.2/0.3, dsn=2.0.0, status=sent (250 ok)")
	l.Add("2026-10-12 11:00:02", "qmgr", "F000000003: removed")
	// 4. Message already queued before the log: deferred, then expired.
	l.Add("2026-10-12 12:00:00", "qmgr", "AB00000004: from=<old@example.org>, size=700, nrcpt=1 (queue active)")
	l.Add("2026-10-12 12:00:30", "smtp", "AB00000004: to=<u@example.net>, relay=none, delay=432000, delays=431970/0/30/0, dsn=4.4.1, status=deferred (connect to mx.example.net[203.0.113.7]:25: Connection timed out)")
	l.Add("2026-10-12 12:00:30", "qmgr", "AB00000004: from=<old@example.org>, status=expired, returned to sender")
	l.Add("2026-10-12 12:00:30", "cleanup", "AC00000004: message-id=<h4@mx1.example.org>")
	l.Add("2026-10-12 12:00:30", "bounce", "AB00000004: sender non-delivery notification: AC00000004")
	l.Add("2026-10-12 12:00:30", "qmgr", "AC00000004: from=<>, size=3000, nrcpt=1 (queue active)")
	l.Add("2026-10-12 12:00:30", "qmgr", "AB00000004: removed")
	l.Add("2026-10-12 12:00:31", "smtp", "AC00000004: to=<old@example.org>, relay=mx.example.org[192.0.2.5]:25, delay=1, delays=0/0/0.5/0.5, dsn=2.0.0, status=sent (250 ok)")
	l.Add("2026-10-12 12:00:31", "qmgr", "AC00000004: removed")
	// 5. Rejections: without queue ID (5xx) and milter after DATA (4xx).
	l.Add("2026-10-12 13:00:00", "smtpd", "NOQUEUE: reject: RCPT from unknown[203.0.113.9]: 554 5.7.1 <a@example.com>: Relay access denied; from=<s@example.net> to=<a@example.com> proto=ESMTP helo=<h>")
	l.Add("2026-10-12 13:01:00", "smtpd", "AD00000005: client=mail.example.com[198.51.100.5]")
	l.Add("2026-10-12 13:01:00", "cleanup", "AD00000005: message-id=<i5@example.com>")
	l.Add("2026-10-12 13:01:01", "cleanup", "AD00000005: milter-reject: END-OF-MESSAGE from mail.example.com[198.51.100.5]: 4.7.1 Try again later; from=<s@example.com> to=<b@example.org> proto=ESMTP helo=<mail.example.com>")
	l.Add("2026-10-12 13:02:00", "smtpd", "warning: unknown[203.0.113.50]: SASL LOGIN authentication failed: UGFzc3dvcmQ6")
	// 6. Queue ID reused after removal.
	l.SMTPMessage("2026-10-12 14:00:00", "A000000001", "B000000009", "carol@example.org", 1500, "w@example.com")
	// 7. Accepted just before midnight, delivered the next day.
	l.SMTPMessage("2026-10-12 23:59:58", "AE00000007", "AF00000007", "dave@example.org", 800, "v@example.com")
	l.Add("2026-10-13 00:30:00", "anvil", "statistics: max cache size 1 at x")
	return l
}

func totals(t *testing.T, a *app.App, days ...string) query.Metrics {
	t.Helper()
	q := &query.Q{DB: a.DB, Loc: a.Cfg.Location, Gap: 3600}
	m, err := q.Totals(days)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func expect(t *testing.T, m query.Metrics, want map[string]float64) {
	t.Helper()
	for k, v := range want {
		if m.Get(k) != v {
			t.Errorf("%s = %v, want %v", k, m.Get(k), v)
		}
	}
}

var day12 = map[string]float64{
	ag.MAccepted: 5, ag.MAcceptedSMTP: 4, ag.MAcceptedLocal: 1, ag.MRecipients: 6, ag.MBytes: 1000 + 2000 + 500 + 1500 + 800,
	ag.MSent: 6, ag.MSentNotif: 2, ag.MDeferred: 2, ag.MBounced: 1, ag.MExpiredRcpt: 1, ag.MExpiredMsg: 1,
	ag.MFinalFailures: 2, ag.MNotifications: 2, ag.MRejects: 2, ag.MRejectsTemp: 1, ag.MRejectsPerm: 1,
	ag.MSentAfterDefer: 1, ag.MFilterHandoff: 5, ag.MSASLFail: 1, ag.MSASLOK: 1, ag.MAttempts: 9,
}

func TestScenarioCounts(t *testing.T) {
	env := testutil.NewEnv(t)
	testutil.WriteFile(t, env.LogPath("maillog"), scenario(t).String(), time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	expect(t, totals(t, a, "2026-10-12"), day12)
	expect(t, totals(t, a, "2026-10-13"), map[string]float64{ag.MSent: 1, ag.MAccepted: 0, ag.MDelayCount: 1})

	// Recipient-level outcomes.
	rows, err := a.DB.Query(`SELECT status, COUNT(*) FROM recipients GROUP BY status`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		rows.Scan(&s, &n)
		got[s] = n
	}
	rows.Close()
	want := map[string]int{"filtered": 5, "delivered": 7, "failed": 1, "expired": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("recipients %s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	// The pre-existing message is flagged, not counted as accepted.
	var flagged int
	a.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE qid='AB00000004' AND accept_observed=0 AND flags LIKE '%start_not_observed%'`).Scan(&flagged)
	if flagged != 1 {
		t.Error("pre-existing instance not flagged")
	}
	// The reused queue ID produced two lifecycles.
	var n int
	a.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE qid='A000000001'`).Scan(&n)
	if n != 2 {
		t.Errorf("A000000001 lifecycles = %d", n)
	}
	r, err := verify.Run(a.DB, a.Cfg.Location, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Checks {
		if !c.OK && !c.Warning {
			t.Errorf("verify %s: %s", c.Name, c.Detail)
		}
	}
}

func eventsCount(t *testing.T, a *app.App) int {
	var n int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRepeatedImportIsIdempotent(t *testing.T) {
	env := testutil.NewEnv(t)
	testutil.WriteFile(t, env.LogPath("maillog"), scenario(t).String(), time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	a := env.Open(t)
	defer a.Close()
	for i := 0; i < 3; i++ {
		sum, err := a.Import(ctx, "test", []string{env.LogPath("maillog")})
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && (sum.Events != 0 || sum.Files[0].Skipped == "") {
			t.Errorf("run %d: %d new events, skipped=%q", i, sum.Events, sum.Files[0].Skipped)
		}
	}
	expect(t, totals(t, a, "2026-10-12"), day12)
	// Rebuild from stored events gives the same numbers.
	if _, _, err := a.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	expect(t, totals(t, a, "2026-10-12"), day12)
}

// TestRotationGzipAndCrossFileCorrelation splits the scenario over a rotated
// compressed file and the current file, with a message whose lifecycle spans
// both, and checks that the result equals a single-file import.
func TestRotationGzipAndCrossFileCorrelation(t *testing.T) {
	lines := scenario(t).Lines()
	cut := 0
	for i, l := range lines { // cut in the middle of message C (deferred, delivered later)
		if strings.Contains(l, "D000000002: from=<bob@example.org>, size=2090, nrcpt=1 (queue active)") && strings.Contains(l, "10:15:00") {
			cut = i
		}
	}
	if cut == 0 {
		t.Fatal("cut point not found")
	}
	env := testutil.NewEnv(t)
	mt := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	cur := env.LogPath("maillog")
	// First run: the current file holds the first 40 lines.
	testutil.WriteFile(t, cur, strings.Join(lines[:40], "\n")+"\n", mt)
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "update", []string{cur}); err != nil {
		t.Fatal(err)
	}
	// More lines are appended, then logrotate compresses the file and a new
	// one starts. The appended lines were never imported before rotation.
	testutil.WriteFile(t, cur, strings.Join(lines[:cut], "\n")+"\n", mt)
	os.Remove(cur)
	testutil.WriteFile(t, env.LogPath("maillog-20261013.gz"), strings.Join(lines[:cut], "\n")+"\n", mt)
	testutil.WriteFile(t, cur, strings.Join(lines[cut:], "\n")+"\n", mt)
	sum, err := a.Import(ctx, "update", []string{env.LogPath("maillog-20261013.gz"), cur})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.FullHosts) != 0 {
		t.Errorf("unexpected full re-correlation: %v", sum.FullHosts)
	}
	expect(t, totals(t, a, "2026-10-12"), day12)
	if got := eventsCount(t, a); got != storedEvents(t, scenario(t).String()) {
		t.Errorf("events = %d, want %d", got, storedEvents(t, scenario(t).String()))
	}
	// A further run reads nothing.
	sum, err = a.Import(ctx, "update", []string{env.LogPath("maillog-20261013.gz"), cur})
	if err != nil || sum.Events != 0 {
		t.Fatalf("third run: %v events, err %v", sum.Events, err)
	}
}

// storedEvents imports content alone and returns the number of stored events.
func storedEvents(t *testing.T, content string) int {
	env := testutil.NewEnv(t)
	testutil.WriteFile(t, env.LogPath("maillog"), content, time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	return eventsCount(t, a)
}

func TestOverlappingSourcesAndDuplicateLines(t *testing.T) {
	lines := scenario(t).Lines()
	env := testutil.NewEnv(t)
	mt := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	testutil.WriteFile(t, env.LogPath("maillog"), strings.Join(lines, "\n")+"\n", mt)
	// A second collector file overlapping the middle of the first one,
	// with a different first line (so a different source).
	testutil.WriteFile(t, env.LogPath("maillog.copy"), "Oct 12 09:59:59 mx1 kernel: unrelated\n"+strings.Join(lines[20:60], "\n")+"\n", mt)
	a := env.Open(t)
	defer a.Close()
	sum, err := a.Import(ctx, "test", []string{env.LogPath("maillog"), env.LogPath("maillog.copy")})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Duplicates == 0 {
		t.Error("overlapping lines not detected as duplicates")
	}
	expect(t, totals(t, a, "2026-10-12"), day12)
}

func TestIdenticalLinesInSameSecondAreKept(t *testing.T) {
	l := testutil.NewLog(t)
	for i := 0; i < 3; i++ {
		l.Add("2026-10-12 10:00:00", "smtpd", "connect from unknown[192.0.2.1]")
	}
	env := testutil.NewEnv(t)
	path := env.LogPath("maillog")
	testutil.WriteFile(t, path, l.String(), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{path}); err != nil {
		t.Fatal(err)
	}
	// Append a fourth identical line in the same second: still a new line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(l.Lines()[0] + "\n")
	f.Close()
	if _, err := a.Import(ctx, "test", []string{path}); err != nil {
		t.Fatal(err)
	}
	expect(t, totals(t, a, "2026-10-12"), map[string]float64{ag.MConnections: 4})
}

func TestBackfillTriggersRecorrelation(t *testing.T) {
	lines := scenario(t).Lines()
	env := testutil.NewEnv(t)
	mt := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	half := len(lines) / 2
	testutil.WriteFile(t, env.LogPath("maillog"), strings.Join(lines[half:], "\n")+"\n", mt)
	testutil.WriteFile(t, env.LogPath("maillog.old"), strings.Join(lines[:half], "\n")+"\n", mt)
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	sum, err := a.Import(ctx, "test", []string{env.LogPath("maillog.old")})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.FullHosts) != 1 {
		t.Errorf("expected full re-correlation, got %v", sum.FullHosts)
	}
	expect(t, totals(t, a, "2026-10-12"), day12)
}

func TestIncompleteLifecycleCompletedLater(t *testing.T) {
	lines := scenario(t).Lines()
	env := testutil.NewEnv(t)
	path := env.LogPath("maillog")
	mt := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	// Stop after the deferral of message C, with a partial (unterminated) line.
	var cut int
	for i, l := range lines {
		if strings.Contains(l, "status=deferred (connect to mx.example.net") && strings.Contains(l, "D000000002") {
			cut = i + 1
		}
	}
	partial := lines[cut][:20]
	testutil.WriteFile(t, path, strings.Join(lines[:cut], "\n")+"\n"+partial, mt)
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{path}); err != nil {
		t.Fatal(err)
	}
	var pending int
	a.DB.QueryRow(`SELECT COUNT(*) FROM recipients WHERE status='pending'`).Scan(&pending)
	if pending != 1 {
		t.Errorf("pending recipients = %d, want 1", pending)
	}
	// The writer completes the line and the rest of the log.
	testutil.WriteFile(t, path, strings.Join(lines, "\n")+"\n", mt)
	if _, err := a.Import(ctx, "test", []string{path}); err != nil {
		t.Fatal(err)
	}
	expect(t, totals(t, a, "2026-10-12"), day12)
	a.DB.QueryRow(`SELECT COUNT(*) FROM recipients WHERE status='pending'`).Scan(&pending)
	if pending != 0 {
		t.Errorf("pending recipients after completion = %d", pending)
	}
}

func TestYearRollover(t *testing.T) {
	l := testutil.NewLog(t)
	l.SMTPMessage("2026-12-31 23:59:59", "A1A1A1A1A1", "B1B1B1B1B1", "a@example.org", 100, "b@example.com")
	l.SMTPMessage("2027-01-01 00:00:05", "A2A2A2A2A2", "B2B2B2B2B2", "a@example.org", 100, "b@example.com")
	env := testutil.NewEnv(t)
	// mtime after the last line: Dec lines must be 2026, Jan lines 2027.
	testutil.WriteFile(t, env.LogPath("maillog"), l.String(), time.Date(2027, 1, 1, 1, 0, 0, 0, time.UTC))
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	expect(t, totals(t, a, "2026-12-31"), map[string]float64{ag.MAccepted: 1})
	expect(t, totals(t, a, "2027-01-01"), map[string]float64{ag.MAccepted: 1, ag.MSent: 2})
}

func TestDSTFallBackDay(t *testing.T) {
	// 25 Oct 2026: 02:00-02:59 happens twice. Hour 02 aggregates both.
	l := testutil.NewLog(t)
	l.Raw("Oct 25 02:30:00 mx1 postfix/smtpd[1]: connect from unknown[192.0.2.1]") // CEST
	l.Raw("Oct 25 02:59:59 mx1 postfix/smtpd[1]: connect from unknown[192.0.2.1]") // CEST
	l.Raw("Oct 25 02:10:00 mx1 postfix/smtpd[1]: connect from unknown[192.0.2.1]") // CET (after fall back)
	l.Raw("Oct 25 03:10:00 mx1 postfix/smtpd[1]: connect from unknown[192.0.2.1]")
	env := testutil.NewEnv(t)
	testutil.WriteFile(t, env.LogPath("maillog"), l.String(), time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	q := &query.Q{DB: a.DB, Loc: a.Cfg.Location, Gap: 3600}
	byHour, err := q.ByHour([]string{"2026-10-25"})
	if err != nil {
		t.Fatal(err)
	}
	if byHour[2].Get(ag.MConnections) != 3 || byHour[3].Get(ag.MConnections) != 1 {
		t.Errorf("hour 02 = %v, hour 03 = %v", byHour[2], byHour[3])
	}
	var span int64
	a.DB.QueryRow(`SELECT MAX(end_ts) - MIN(start_ts) FROM segments`).Scan(&span)
	if span != 3600+40*60 { // 02:30 CEST -> 03:10 CET = 1 h 40 min
		t.Errorf("observed span = %d s", span)
	}
}

func TestReinjectionLinkingWithIdenticalMessages(t *testing.T) {
	// Two messages with the same sender, message-id and nrcpt (an
	// application reusing message-ids), reinjected in order.
	l := testutil.NewLog(t)
	for i, q := range []string{"AAAA000001", "AAAA000002"} {
		ts := time.Date(2026, 10, 12, 10, 0, i, 0, l.Loc).Format("2006-01-02 15:04:05")
		l.Add(ts, "smtpd", q+": client=app.example.net[192.0.2.10]")
		l.Add(ts, "cleanup", q+": message-id=<same@example.net>")
		l.Add(ts, "qmgr", q+": from=<app@example.net>, size=100, nrcpt=1 (queue active)")
	}
	for i, pair := range [][2]string{{"AAAA000001", "BBBB000001"}, {"AAAA000002", "BBBB000002"}} {
		ts := time.Date(2026, 10, 12, 10, 0, 2+i, 0, l.Loc).Format("2006-01-02 15:04:05")
		l.Add(ts, "pickup", pair[1]+": uid=97 from=<app@example.net>")
		l.Add(ts, "cleanup", pair[1]+": message-id=<same@example.net>")
		l.Add(ts, "qmgr", pair[1]+": from=<app@example.net>, size=190, nrcpt=1 (queue active)")
		l.Add(ts, "pipe", pair[0]+": to=<u@example.org>, relay=filter, delay=2, delays=1/0/0/1, dsn=2.0.0, status=sent (delivered via filter service)")
	}
	env := testutil.NewEnv(t)
	testutil.WriteFile(t, env.LogPath("maillog"), l.String(), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	rows, _ := a.DB.Query(`SELECT c.qid, p.qid FROM instances c JOIN instances p ON p.id = c.parent_id ORDER BY c.qid`)
	defer rows.Close()
	want := map[string]string{"BBBB000001": "AAAA000001", "BBBB000002": "AAAA000002"}
	n := 0
	for rows.Next() {
		var c, p string
		rows.Scan(&c, &p)
		if want[c] != p {
			t.Errorf("%s linked to %s", c, p)
		}
		n++
	}
	if n != 2 {
		t.Errorf("%d links", n)
	}
	expect(t, totals(t, a, "2026-10-12"), map[string]float64{ag.MAccepted: 2})
}

func TestInvalidationMarksDependentPages(t *testing.T) {
	env := testutil.NewEnv(t)
	testutil.WriteFile(t, env.LogPath("maillog"), scenario(t).String(), time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	a := env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	rows, _ := a.DB.Query(`SELECT kind || ' ' || key FROM dirty_pages`)
	got := map[string]bool{}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		got[s] = true
	}
	rows.Close()
	for _, w := range []string{"index index", "day 2026-10-12", "day 2026-10-13", "day 2026-10-11", "day 2026-10-14",
		"week 2026-W42", "week 2026-W41", "month 2026-10", "month 2026-09", "year 2026"} {
		if !got[w] {
			t.Errorf("missing dirty page %s (have %v)", w, got)
		}
	}
	if got["day 2026-10-20"] {
		t.Error("unrelated day invalidated")
	}
}

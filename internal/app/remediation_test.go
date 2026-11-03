// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package app_test

import (
	"github.com/fdefilippo/postfix-report/internal/aggregate"
	"github.com/fdefilippo/postfix-report/internal/testutil"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSameSecondBackfillMatchesRebuild(t *testing.T) {
	env := testutil.NewEnv(t)
	env.Cfg.Input.Year = 2026
	a := env.Open(t)
	defer a.Close()
	active := testutil.NewLog(t).Add("2026-10-12 10:00:00", "qmgr", "A111111111: from=<a@example.org>, size=100, nrcpt=1 (queue active)")
	prefix := testutil.NewLog(t).Add("2026-10-12 09:59:00", "anvil", "statistics: max cache size 1 at x").Add("2026-10-12 10:00:00", "smtpd", "A111111111: client=app.example.org[192.0.2.1]").Add("2026-10-12 10:00:00", "cleanup", "A111111111: message-id=<a@example.org>")
	mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	for i, l := range []string{active.String(), prefix.String()} {
		p := env.LogPath(itoaRemediation(i))
		testutil.WriteFile(t, p, l, mt)
		s, err := a.Import(ctx, "test", []string{p})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && len(s.FullHosts) != 1 {
			t.Fatal("same-second earlier source did not trigger replay")
		}
	}
	before := totals(t, a, "2026-10-12")
	if before.Get(aggregate.MAccepted) != 1 {
		t.Fatalf("accepted=%v", before)
	}
	if _, _, err := a.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	after := totals(t, a, "2026-10-12")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("increment=%v rebuild=%v", before, after)
	}
}
func itoaRemediation(i int) string {
	if i == 0 {
		return "a"
	}
	return "b"
}

func TestForwardAppendSameSecondAvoidsReplay(t *testing.T) {
	env := testutil.NewEnv(t)
	a := env.Open(t)
	defer a.Close()
	p := env.LogPath("mail")
	mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	l := testutil.NewLog(t).Add("2026-10-12 10:00:00", "smtpd", "A111111111: client=app.example.org[192.0.2.1]")
	testutil.WriteFile(t, p, l.String(), mt)
	if _, err := a.Import(ctx, "test", []string{p}); err != nil {
		t.Fatal(err)
	}
	l.Add("2026-10-12 10:00:00", "qmgr", "A111111111: from=<a@example.org>, size=100, nrcpt=1 (queue active)")
	testutil.WriteFile(t, p, l.String(), mt)
	s, err := a.Import(ctx, "test", []string{p})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.FullHosts) != 0 {
		t.Fatalf("forward append reset host: %v", s.FullHosts)
	}
	if totals(t, a, "2026-10-12").Get(aggregate.MAccepted) != 1 {
		t.Fatal("forward append lost acceptance")
	}
}

func TestPendingNotificationRestartAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name, childTime string
		notified        bool
	}{{"within window", "2026-10-12 10:00:02", true}, {"expired", "2026-10-12 10:01:02", false}} {
		t.Run(tc.name, func(t *testing.T) {
			env := testutil.NewEnv(t)
			p := env.LogPath("mail")
			mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
			l := testutil.NewLog(t).Add("2026-10-12 10:00:00", "qmgr", "A111111111: from=<a@example.org>, size=100, nrcpt=1 (queue active)").Add("2026-10-12 10:00:01", "bounce", "A111111111: sender non-delivery notification: B111111111").Add("2026-10-12 10:00:01", "qmgr", "A111111111: removed")
			testutil.WriteFile(t, p, l.String(), mt)
			a := env.Open(t)
			if _, err := a.Import(ctx, "test", []string{p}); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := a.DB.QueryRow(`SELECT COUNT(*) FROM pending_notifications`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("pending=%d %v", n, err)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			l.Add(tc.childTime, "cleanup", "B111111111: message-id=<bounce@example.org>").Add(tc.childTime, "qmgr", "B111111111: from=<>, size=100, nrcpt=1 (queue active)")
			testutil.WriteFile(t, p, l.String(), mt)
			a = env.Open(t)
			defer a.Close()
			if _, err := a.Import(ctx, "test", []string{p}); err != nil {
				t.Fatal(err)
			}
			var origin string
			var parent int64
			if err := a.DB.QueryRow(`SELECT origin,COALESCE(notif_parent_id,0) FROM instances WHERE qid='B111111111'`).Scan(&origin, &parent); err != nil {
				t.Fatal(err)
			}
			if (origin == "notification" && parent != 0) != tc.notified {
				t.Fatalf("origin=%s parent=%d", origin, parent)
			}
			before := totals(t, a, "2026-10-12")
			if _, _, err := a.Rebuild(ctx); err != nil {
				t.Fatal(err)
			}
			if after := totals(t, a, "2026-10-12"); !reflect.DeepEqual(before, after) {
				t.Fatalf("increment=%v rebuild=%v", before, after)
			}
			if err := a.DB.QueryRow(`SELECT COUNT(*) FROM pending_notifications`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("consumed/expired pending=%d %v", n, err)
			}
		})
	}
}

func TestPendingNotificationInstanceIsolation(t *testing.T) {
	env := testutil.NewEnv(t)
	p := env.LogPath("mail")
	mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	first := testutil.NewLog(t).Add("2026-10-12 10:00:00", "qmgr", "A111111111: from=<a@example.org>, size=100, nrcpt=1 (queue active)").Add("2026-10-12 10:00:01", "bounce", "A111111111: sender non-delivery notification: B111111111").String()
	first += strings.ReplaceAll(first, "postfix/", "postfix-other/")
	testutil.WriteFile(t, p, first, mt)
	a := env.Open(t)
	if _, err := a.Import(ctx, "test", []string{p}); err != nil {
		t.Fatal(err)
	}
	a.Close()
	child := testutil.NewLog(t).Add("2026-10-12 10:00:02", "cleanup", "B111111111: message-id=<bounce@example.org>").Add("2026-10-12 10:00:02", "qmgr", "B111111111: from=<>, size=100, nrcpt=1 (queue active)").String()
	testutil.WriteFile(t, p, first+child+strings.ReplaceAll(child, "postfix/", "postfix-other/"), mt)
	a = env.Open(t)
	defer a.Close()
	if _, err := a.Import(ctx, "test", []string{p}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM instances c JOIN instances p ON c.notif_parent_id=p.id WHERE c.qid='B111111111' AND c.origin='notification' AND c.inst=p.inst`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("isolated children=%d %v", n, err)
	}
}

func TestUpgradeRecoversUnpersistedNotification(t *testing.T) {
	env := testutil.NewEnv(t)
	a := env.Open(t)
	defer a.Close()
	p := env.LogPath("mail")
	mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	l := testutil.NewLog(t).Add("2026-10-12 10:00:00", "qmgr", "A111111111: from=<a@example.org>, size=100, nrcpt=1 (queue active)").Add("2026-10-12 10:00:01", "bounce", "A111111111: sender non-delivery notification: B111111111")
	testutil.WriteFile(t, p, l.String(), mt)
	if _, err := a.Import(ctx, "test", []string{p}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(`DELETE FROM pending_notifications; INSERT INTO meta(key,value) VALUES('correlation_pending_upgrade','1') ON CONFLICT(key) DO UPDATE SET value='1'`); err != nil {
		t.Fatal(err)
	}
	l.Add("2026-10-12 10:00:02", "cleanup", "B111111111: message-id=<bounce@example.org>").Add("2026-10-12 10:00:02", "qmgr", "B111111111: from=<>, size=100, nrcpt=1 (queue active)")
	testutil.WriteFile(t, p, l.String(), mt)
	s, err := a.Import(ctx, "test", []string{p})
	if err != nil || len(s.FullHosts) != 1 {
		t.Fatalf("upgrade replay=%+v %v", s, err)
	}
	var n int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE qid='B111111111' AND origin='notification'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("upgrade child=%d %v", n, err)
	}
}

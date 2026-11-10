// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/correlate"
	"github.com/fdefilippo/postfix-report/internal/store"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

func TestNotificationWindowCancellationRetriesReconfiguration(t *testing.T) {
	e := testutil.NewEnv(t)
	p := e.LogPath("mail")
	l := testutil.NewLog(t).Add("2026-10-12 10:00:00", "qmgr", "A111111111: from=<sender@example.org>, size=100, nrcpt=1 (queue active)").Add("2026-10-12 10:00:01", "bounce", "A111111111: sender non-delivery notification: B111111111").Add("2026-10-12 10:01:02", "cleanup", "B111111111: message-id=<bounce@example.org>").Add("2026-10-12 10:01:02", "qmgr", "B111111111: from=<>, size=100, nrcpt=1 (queue active)")
	testutil.WriteFile(t, p, l.String(), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	a := e.Open(t)
	defer func() { _ = a.Close() }()
	if _, err := a.Import(ctx, "test", []string{p}); err != nil {
		t.Fatal(err)
	}
	e.Cfg.Correlation.NotificationWindow.Duration = 120 * time.Second
	c := &correlate.Correlator{DB: a.DB, Cfg: e.Cfg, Log: e.Log}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Run(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	marker, err := store.GetMeta(a.DB, "correlation_notification_window")
	if err != nil || marker != "60" {
		t.Fatalf("premature marker %q %v", marker, err)
	}
	if _, err := a.Import(ctx, "test", []string{p}); err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := a.DB.QueryRow(`SELECT origin FROM instances WHERE qid='B111111111'`).Scan(&origin); err != nil || origin != "notification" {
		t.Fatalf("retry origin=%q %v", origin, err)
	}
	marker, err = store.GetMeta(a.DB, "correlation_notification_window")
	if err != nil || marker != "120" {
		t.Fatalf("retry marker=%q %v", marker, err)
	}
}

func TestNotificationAlreadyOpenChildRetainsExplicitLink(t *testing.T) {
	e := testutil.NewEnv(t)
	p := e.LogPath("mail")
	l := testutil.NewLog(t).Add("2026-10-12 10:00:00", "cleanup", "B111111111: message-id=<bounce@example.org>").Add("2026-10-12 10:00:00", "qmgr", "B111111111: from=<>, size=100, nrcpt=1 (queue active)")
	mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	testutil.WriteFile(t, p, l.String(), mt)
	a := e.Open(t)
	if _, err := a.Import(ctx, "test", []string{p}); err != nil {
		_ = a.Close()
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// The explicit bounce announcement can follow an already open child by more
	// than the pending-announcement window. It is still an explicit QID link.
	l.Add("2026-10-12 10:02:00", "qmgr", "A111111111: from=<sender@example.org>, size=100, nrcpt=1 (queue active)").Add("2026-10-12 10:02:01", "bounce", "A111111111: sender non-delivery notification: B111111111")
	testutil.WriteFile(t, p, l.String(), mt)
	a = e.Open(t)
	defer func() { _ = a.Close() }()
	if _, err := a.Import(ctx, "test", []string{p}); err != nil {
		t.Fatal(err)
	}
	var origin string
	var parent int64
	if err := a.DB.QueryRow(`SELECT origin,COALESCE(notif_parent_id,0) FROM instances WHERE qid='B111111111'`).Scan(&origin, &parent); err != nil || origin != "notification" || parent == 0 {
		t.Fatalf("child=%q parent=%d %v", origin, parent, err)
	}
	before := totals(t, a, "2026-10-12")
	if _, _, err := a.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if after := totals(t, a, "2026-10-12"); !reflect.DeepEqual(before, after) {
		t.Fatalf("increment=%v rebuild=%v", before, after)
	}
}

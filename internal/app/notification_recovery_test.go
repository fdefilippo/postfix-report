// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package app_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/correlate"
	"github.com/fdefilippo/postfix-report/internal/store"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

func TestNotificationWindowRevertAfterPartialReplay(t *testing.T) {
	for _, mode := range []string{"incremental", "rebuild"} {
		t.Run(mode, func(t *testing.T) {
			e := testutil.NewEnv(t)
			p := e.LogPath("mail")
			var logs strings.Builder
			for _, host := range []string{"mx1", "mx2"} {
				l := testutil.NewLog(t)
				l.Host = host
				l.Add("2026-10-12 10:00:00", "qmgr", "A111111111: from=<sender@example.org>, size=100, nrcpt=1 (queue active)").
					Add("2026-10-12 10:00:01", "bounce", "A111111111: sender non-delivery notification: B111111111").
					Add("2026-10-12 10:01:02", "cleanup", "B111111111: message-id=<bounce@example.org>").
					Add("2026-10-12 10:01:02", "qmgr", "B111111111: from=<>, size=100, nrcpt=1 (queue active)")
				_, _ = logs.WriteString(l.String())
			}
			testutil.WriteFile(t, p, logs.String(), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
			a := e.Open(t)
			defer func() { _ = a.Close() }()
			if _, err := a.Import(ctx, "test", []string{p}); err != nil {
				t.Fatal(err)
			}
			// Fail the second host only after the first host's complete replay
			// committed. This exercises persistent mixed state, not pre-cancel.
			if _, err := a.DB.Exec(`CREATE TRIGGER fail_second_host BEFORE DELETE ON corr_state WHEN OLD.host='mx2' BEGIN SELECT RAISE(FAIL,'synthetic host2 replay failure'); END`); err != nil {
				t.Fatal(err)
			}
			e.Cfg.Correlation.NotificationWindow.Duration = 120 * time.Second
			c := &correlate.Correlator{DB: a.DB, Cfg: e.Cfg, Log: e.Log}
			var err error
			if mode == "rebuild" {
				_, err = c.Rebuild(ctx)
			} else {
				_, err = c.Run(ctx)
			}
			if err == nil || !strings.Contains(err.Error(), "synthetic host2 replay failure") {
				t.Fatalf("expected partial replay failure: %v", err)
			}
			var firstOrigin string
			if err := a.DB.QueryRow(`SELECT origin FROM instances WHERE host='mx1' AND qid='B111111111'`).Scan(&firstOrigin); err != nil || firstOrigin != "notification" {
				t.Fatalf("first host was not committed at 120s: %q %v", firstOrigin, err)
			}
			if marker, err := store.GetMeta(a.DB, "correlation_notification_window"); err != nil || marker != "60" {
				t.Fatalf("completed marker advanced: %q %v", marker, err)
			}
			if _, err := a.DB.Exec(`DROP TRIGGER fail_second_host`); err != nil {
				t.Fatal(err)
			}
			// Revert to the last successful configuration before retrying. A
			// completed marker of 60 alone cannot detect the first host's 120s.
			e.Cfg.Correlation.NotificationWindow.Duration = 60 * time.Second
			if _, err := a.Import(ctx, "test", []string{p}); err != nil {
				t.Fatal(err)
			}
			var notifications int
			if err := a.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE qid='B111111111' AND origin='notification'`).Scan(&notifications); err != nil || notifications != 0 {
				t.Fatalf("reverted window retained mixed host state: notifications=%d %v", notifications, err)
			}
			if marker, err := store.GetMeta(a.DB, "correlation_notification_window_pending"); err != nil || marker != "" {
				t.Fatalf("pending replay not completed: %q %v", marker, err)
			}
			before := totals(t, a, "2026-10-12")
			if _, _, err := a.Rebuild(ctx); err != nil {
				t.Fatal(err)
			}
			if after := totals(t, a, "2026-10-12"); !reflect.DeepEqual(before, after) {
				t.Fatalf("recovered=%v rebuild=%v", before, after)
			}
		})
	}
}

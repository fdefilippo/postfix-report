// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package verify

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/store"
)

func TestMissingVerificationTableNeverPasses(t *testing.T) {
	for _, tc := range []struct{ table, check string }{
		{"dirty_days", "aggregati in attesa di ricalcolo"},
		{"dirty_pages", "report in attesa di rigenerazione"},
		{"unknown_lines", "righe Postfix non riconosciute"},
	} {
		t.Run(tc.table, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "report.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func(cleanup func() error) { _ = cleanup() }(db.Close)
			if report, err := Run(db, time.UTC, ""); err != nil || report.Failed() {
				t.Fatalf("healthy: %v %v", report, err)
			}
			if _, err := db.Exec("DROP TABLE " + tc.table); err != nil {
				t.Fatal(err)
			}
			if _, err := Run(db, time.UTC, ""); err == nil || !strings.Contains(err.Error(), tc.check) {
				t.Fatalf("missing %s: %v", tc.table, err)
			}
		})
	}
}

func TestSchemaMustMatchBinary(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func(cleanup func() error) { _ = cleanup() }(db.Close)
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,name,applied_at) VALUES(999,'future',0)`); err != nil {
		t.Fatal(err)
	}
	r, err := Run(db, time.UTC, "")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Failed() {
		t.Fatal("future schema passed")
	}
}

func TestIncompleteReconciliationNeverPasses(t *testing.T) {
	for _, tc := range []struct{ key, check string }{
		{"correlation_notification_window_pending", "ricorrelazione notifiche incompleta"},
		{"report_publication_pending", "riconciliazione pubblicazione incompleta"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "report.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if report, err := Run(db, time.UTC, ""); err != nil || report.Failed() {
				t.Fatalf("healthy: %v %v", report, err)
			}
			if err := store.SetMeta(db, tc.key, "pending"); err != nil {
				t.Fatal(err)
			}
			report, err := Run(db, time.UTC, "")
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			for _, check := range report.Checks {
				if check.Name == tc.check && !check.OK && !check.Warning {
					failed = true
				}
			}
			if !report.Failed() || !failed {
				t.Fatalf("pending operation passed: %+v", report)
			}
			if _, err := db.Exec(`DELETE FROM meta WHERE key=?`, tc.key); err != nil {
				t.Fatal(err)
			}
			if report, err := Run(db, time.UTC, ""); err != nil || report.Failed() {
				t.Fatalf("completed: %v %v", report, err)
			}
		})
	}
}

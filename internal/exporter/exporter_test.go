// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package exporter_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/document"
	"github.com/fdefilippo/postfix-report/internal/exporter"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

func fixture(t *testing.T) *testutil.Env {
	t.Helper()
	e := testutil.NewEnv(t)
	l := testutil.NewLog(t)
	l.SMTPMessage("2026-10-25 10:00:00", "ABC01", "DEF02", "alice@example.org", 123, "bob@example.net")
	testutil.WriteFile(t, e.LogPath("maillog"), l.String(), time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	a := e.Open(t)
	if _, err := a.Import(context.Background(), "test", []string{e.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AnonKey(); err != nil {
		t.Fatal(err)
	}
	a.Close()
	return e
}

func TestAllPeriodsLanguagesAndPrivacy(t *testing.T) {
	e := fixture(t)
	e.Cfg.Privacy.AnonymizeEmail = true
	e.Cfg.Privacy.AnonymizeIP = true
	for _, lang := range []string{"it", "en"} {
		e.Cfg.Output.Language = lang
		for k, key := range map[period.Kind]string{period.Day: "2026-10-25", period.Week: "2026-W43", period.Month: "2026-10", period.Year: "2026"} {
			t.Run(lang+"/"+string(k), func(t *testing.T) {
				p, err := period.Parse(k, key, e.Cfg.Location)
				if err != nil {
					t.Fatal(err)
				}
				d, err := exporter.Prepare(context.Background(), e.Cfg, p, "test")
				if err != nil {
					t.Fatal(err)
				}
				var content strings.Builder
				for _, b := range d.Blocks {
					content.WriteString(b.Text)
					for _, row := range b.Rows {
						content.WriteString(strings.Join(row, " "))
					}
				}
				for _, secret := range []string{"alice@example.org", "bob@example.net", "192.0.2.10"} {
					if strings.Contains(content.String(), secret) {
						t.Fatal("unmasked", secret)
					}
				}
				expected := []string{"Messaggi accettati: 1", "Destinatari: 1", "Tempi di consegna", "Europe/Rome"}
				if lang == "en" {
					expected = []string{"Accepted messages: 1", "Recipients: 1", "Delivery times", "Europe/Rome"}
				}
				for _, value := range expected {
					if !strings.Contains(content.String(), value) {
						t.Errorf("missing metric/section %q", value)
					}
				}
				for _, ui := range []string{"Clic su un", "Click a ", "sortable and filterable", "ordinabili e filtrabili"} {
					if strings.Contains(content.String(), ui) {
						t.Errorf("interactive instruction in document: %q", ui)
					}
				}
				if len(d.Blocks) < 10 {
					t.Fatal("incomplete document")
				}
				for _, format := range []string{"pdf", "odt"} {
					b, err := document.Render(context.Background(), d, format)
					if err != nil {
						t.Fatalf("%s: %v", format, err)
					}
					if len(b) < 1000 {
						t.Fatal("empty output")
					}
				}
			})
		}
	}
}

func TestWriterCoordinationAndFailedImport(t *testing.T) {
	e := fixture(t)
	p, _ := period.Parse(period.Day, "2026-10-25", e.Cfg.Location)
	a, err := app.Open(e.Cfg, e.Log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	if _, err := exporter.Prepare(ctx, e.Cfg, p, "test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reader did not respect writer/cancellation: %v", err)
	}
	if _, err = a.DB.Exec(`UPDATE imports SET status='failed'`); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if _, err := exporter.Prepare(context.Background(), e.Cfg, p, "test"); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("failed import accepted: %v", err)
	}
	a = e.Open(t)
	if _, err := a.DB.Exec(`UPDATE imports SET status='ok'`); err != nil {
		t.Fatal(err)
	}
	a.Close()
	d, err := exporter.Prepare(context.Background(), e.Cfg, p, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Rendering a detached model must not retain a reader/import lock.
	a, err = app.Open(e.Cfg, e.Log)
	if err != nil {
		t.Fatal("snapshot retained writer lock", err)
	}
	defer a.Close()
	if _, err := document.Render(context.Background(), d, "pdf"); err != nil {
		t.Fatal(fmt.Errorf("render while writer owns lock: %w", err))
	}
}

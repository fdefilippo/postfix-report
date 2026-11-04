// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package exporter_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

func TestArchiveSelectionCompositionAndContainers(t *testing.T) {
	e := fixture(t)
	e.Cfg.Privacy.AnonymizeEmail = true
	e.Cfg.Privacy.AnonymizeIP = true
	for _, lang := range []string{"it", "en"} {
		e.Cfg.Output.Language = lang
		plan, err := exporter.PreviewArchive(context.Background(), e.Cfg, exporter.ArchiveSelection{}, "test")
		if err != nil || plan.Total != 4 || !plan.Allowed || plan.From != "2026-10-25" || plan.To != plan.From {
			t.Fatalf("plan: %+v %v", plan, err)
		}
		d, err := exporter.PrepareArchive(context.Background(), e.Cfg, exporter.ArchiveSelection{}, "test")
		if err != nil {
			t.Fatal(err)
		}
		if lang == "en" && (d.Title != "Complete archive" || d.ContentsTitle != "Contents" || !strings.HasPrefix(d.CoverNote, "Selected range:")) {
			t.Fatal("archive translation missing")
		}
		var keys []string
		var text strings.Builder
		for _, b := range d.Blocks {
			if b.Level == 2 {
				keys = append(keys, b.Text)
			}
			text.WriteString(b.Text)
			for _, row := range b.Rows {
				text.WriteString(strings.Join(row, " "))
			}
		}
		for i, key := range []string{"2026", "2026-10", "2026-W43", "2026-10-25"} {
			if len(keys) != 4 || !strings.HasSuffix(keys[i], " · "+key) {
				t.Fatalf("period order: %v", keys)
			}
		}
		metric := "Messaggi accettati: 1"
		if lang == "en" {
			metric = "Accepted messages: 1"
		}
		if strings.Count(text.String(), metric) != 4 {
			t.Fatalf("overlapping counts changed: %s", metric)
		}
		for _, secret := range []string{"alice@example.org", "bob@example.net", "192.0.2.10"} {
			if strings.Contains(text.String(), secret) {
				t.Fatal("archive privacy leak", secret)
			}
		}
		for _, format := range []string{"pdf", "odt"} {
			b, err := document.Render(context.Background(), d, format)
			if err != nil {
				t.Fatal(lang, format, err)
			}
			if format == "pdf" {
				if !bytes.HasPrefix(b, []byte("%PDF-")) || !bytes.Contains(b, []byte("/Outlines")) || !bytes.Contains(b, []byte("/Subtype /Link")) {
					t.Fatal("PDF missing index navigation")
				}
			} else {
				z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
				if err != nil {
					t.Fatal(err)
				}
				f, err := z.Open("content.xml")
				if err != nil {
					t.Fatal(err)
				}
				content, err := io.ReadAll(f)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(content, []byte("<text:table-of-content")) || !bytes.Contains(content, []byte(`text:outline-level="2"`)) || !bytes.Contains(content, []byte("2026-W43")) {
					t.Fatal("ODT missing archive contents")
				}
			}
		}
	}
	dayOnly := exporter.ArchiveSelection{From: "2026-10-25", To: "2026-10-25", Kinds: []period.Kind{period.Day}}
	plan, err := exporter.PreviewArchive(context.Background(), e.Cfg, dayOnly, "test")
	if err != nil || plan.Total != 1 {
		t.Fatalf("day selection: %+v %v", plan, err)
	}
	for _, bad := range []exporter.ArchiveSelection{
		{Kinds: []period.Kind{}}, {Kinds: []period.Kind{period.Day, period.Day}}, {Kinds: []period.Kind{"invalid"}},
		{From: "2026-10-25"}, {From: "2026-10-26", To: "2026-10-25"}, {From: "2026-02-30", To: "2026-10-25"},
	} {
		if bad.Validate(e.Cfg) == nil {
			t.Fatalf("invalid selection accepted: %+v", bad)
		}
	}
	e.Cfg.Reports.Daily = false
	if dayOnly.Validate(e.Cfg) == nil {
		t.Fatal("disabled daily report selected")
	}
	plan, err = exporter.PreviewArchive(context.Background(), e.Cfg, exporter.ArchiveSelection{}, "test")
	if err != nil || plan.Total != 3 {
		t.Fatalf("default includes disabled reports: %+v %v", plan, err)
	}
}

func TestArchiveLimitsGapsAndWholePeriods(t *testing.T) {
	e := fixture(t)
	a := e.Open(t)
	// Extend observed bounds without manufacturing traffic in the intervening gap.
	hi := time.Date(2027, 1, 3, 12, 0, 0, 0, e.Cfg.Location).Unix()
	if _, err := a.DB.Exec(`INSERT INTO segments(source_id,host,start_ts,end_ts) SELECT source_id,host,?,? FROM segments LIMIT 1`, hi, hi+60); err != nil {
		t.Fatal(err)
	}
	a.Close()
	sel := exporter.ArchiveSelection{From: "2027-01-01", To: "2027-01-01"}
	plan, err := exporter.PreviewArchive(context.Background(), e.Cfg, sel, "test")
	if err != nil || plan.Total != 4 {
		t.Fatalf("selection: %+v %v", plan, err)
	}
	d, err := exporter.PrepareArchive(context.Background(), e.Cfg, sel, "test")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range d.Blocks {
		found = found || b.Level == 2 && strings.HasSuffix(b.Text, "2026-W53")
	}
	if !found {
		t.Fatal("ISO week across calendar year not included")
	}
	if _, err = exporter.PreviewArchive(context.Background(), e.Cfg, exporter.ArchiveSelection{From: "2025-01-01", To: "2025-01-02"}, "test"); err == nil {
		t.Fatal("empty range accepted")
	}
	a = e.Open(t)
	hi = time.Date(2027, 12, 31, 12, 0, 0, 0, e.Cfg.Location).Unix()
	if _, err = a.DB.Exec(`UPDATE segments SET end_ts=?`, hi); err != nil {
		t.Fatal(err)
	}
	a.Close()
	plan, err = exporter.PreviewArchive(context.Background(), e.Cfg, exporter.ArchiveSelection{}, "test")
	if err != nil || plan.Allowed || plan.Total <= plan.Limit {
		t.Fatalf("large archive: %+v %v", plan, err)
	}
	if _, err = exporter.PrepareArchive(context.Background(), e.Cfg, exporter.ArchiveSelection{}, "test"); !errors.Is(err, exporter.ErrArchiveLimit) {
		t.Fatalf("archive truncated or rendered beyond limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = exporter.PrepareArchive(ctx, e.Cfg, exporter.ArchiveSelection{}, "test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestArchiveRangeIncludesWholeCalendarReports(t *testing.T) {
	e := testutil.NewEnv(t)
	l := testutil.NewLog(t)
	l.SMTPMessage("2026-10-25 10:00:00", "ABC01", "DEF02", "alice@example.org", 123, "bob@example.net")
	l.SMTPMessage("2026-10-26 10:00:00", "ABC03", "DEF04", "alice@example.org", 123, "bob@example.net")
	testutil.WriteFile(t, e.LogPath("maillog"), l.String(), time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
	a := e.Open(t)
	if _, err := a.Import(context.Background(), "test", []string{e.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AnonKey(); err != nil {
		t.Fatal(err)
	}
	a.Close()
	d, err := exporter.PrepareArchive(context.Background(), e.Cfg, exporter.ArchiveSelection{From: "2026-10-26", To: "2026-10-26"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Year/month retain both messages; selected ISO week/day retain one.
	counts := map[string]int{}
	for _, b := range d.Blocks {
		if strings.HasPrefix(b.Text, "Messaggi accettati:") {
			counts[b.Text]++
		}
		if b.Level == 2 && strings.HasSuffix(b.Text, "2026-10-25") {
			t.Fatal("out-of-range daily report included")
		}
	}
	one, two := 0, 0
	for text, n := range counts {
		if strings.HasPrefix(text, "Messaggi accettati: 1 ·") {
			one += n
		}
		if strings.HasPrefix(text, "Messaggi accettati: 2 ·") {
			two += n
		}
	}
	if one != 2 || two != 2 {
		t.Fatalf("whole periods were clipped or summed: %v", counts)
	}
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

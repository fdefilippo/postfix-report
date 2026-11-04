// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fdefilippo/postfix-report/internal/document"
	"github.com/fdefilippo/postfix-report/internal/period"
)

// Document materializes every section of a period report. The result has no
// database references, so rendering does not hold the import lock or a reader.
func (g *Generator) Document(ctx context.Context, p period.Period) (document.Document, error) {
	var d document.Document
	if !g.enabled(p.Kind) {
		return d, fmt.Errorf("%s reports are disabled", p.Kind)
	}
	if !g.inRange(p) {
		return d, fmt.Errorf("no data in period %s", p.Key)
	}
	pg, err := g.periodPage(p)
	if err != nil {
		return d, err
	}
	d = document.Document{Title: pg.Title, Subtitle: pg.SiteTitle, Language: pg.Lang, Date: g.Now(), Creator: "postfix-report " + g.Version, ContentsTitle: g.tr("Indice"), PageLabel: g.tr("Pagina")}
	d.Paragraph(pg.Subtitle)
	d.Paragraph(g.trf("Fuso orario: %s", g.loc().String()))
	for _, b := range pg.Banners {
		d.Paragraph(b.Text)
	}
	d.Paragraph(g.trf("Dati fino a %s", g.dataAsOf))
	if pg.Privacy != "" {
		d.Paragraph(pg.Privacy)
	}
	table := func(t *Table) {
		if t == nil {
			return
		}
		head := make([]string, len(t.Head))
		for i, c := range t.Head {
			head[i] = c.Text
		}
		var rows [][]string
		for _, row := range t.Rows {
			cells := make([]string, len(row.Cells))
			for i, c := range row.Cells {
				cells[i] = c.Text
				if c.Title != "" && c.Title != c.Text {
					cells[i] += " (" + c.Title + ")"
				}
			}
			rows = append(rows, cells)
		}
		if len(rows) == 0 {
			if t.Empty != "" {
				d.Paragraph(t.Empty)
			} else {
				d.Paragraph(g.tr("Nessun dato."))
			}
			return
		}
		d.Table(head, rows)
	}
	var walk func([]Block, int) error
	walk = func(blocks []Block, level int) error {
		for _, b := range blocks {
			if err := ctx.Err(); err != nil {
				return err
			}
			next := level
			if b.Title != "" {
				d.Heading(min(3, level), b.Title)
				next = min(3, level+1)
			}
			note := b.Note
			switch note {
			case g.tr("Clic su un giorno per aprire il report giornaliero."), g.tr("Clic su un mese per aprire il report mensile."):
				note = ""
			case g.trf("Primi %d valori per categoria; tabelle ordinabili e filtrabili.", g.Cfg.Reports.TopLimit):
				note = g.trf("Primi %d valori per categoria.", g.Cfg.Reports.TopLimit)
			}
			if note != "" {
				d.Paragraph(note)
			}
			switch b.Kind {
			case "kpis":
				for _, k := range b.KPIs {
					parts := []string{k.Label + ": " + k.Value, k.Unit}
					if k.Delta != "" {
						parts = append(parts, k.Delta)
					}
					d.Paragraph(strings.Join(parts, " · "))
					if k.Title != "" {
						d.Paragraph(k.Title)
					}
				}
			case "table":
				table(b.Table)
			case "chart":
				if b.Chart != nil {
					var spec document.Chart
					if err := json.Unmarshal([]byte(b.Chart.JSON), &spec); err != nil {
						return err
					}
					d.Blocks = append(d.Blocks, document.Block{Chart: &spec})
					table(b.Chart.Table)
				}
			case "text":
				d.Paragraph(b.Text)
			}
			if err := walk(b.Children, next); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(pg.Blocks, 1); err != nil {
		return d, err
	}
	d.Paragraph("postfix-report " + g.Version + " · Copyright © 2026 Francesco Defilippo · GNU GPLv3 or later")
	return d, nil
}

// ArchiveDocument composes whole reports without summing overlapping periods.
// All queries use the caller's snapshot; chart rendering happens afterwards.
func (g *Generator) ArchiveDocument(ctx context.Context, periods []period.Period, from, to string) (document.Document, error) {
	d := document.Document{Title: g.tr("Archivio completo"), Subtitle: g.Cfg.Output.Title,
		Language: g.Cfg.Output.Language, Date: g.Now(), Creator: "postfix-report " + g.Version,
		ContentsTitle: g.tr("Indice"), PageLabel: g.tr("Pagina"), ContentsDepth: 2}
	d.HeaderTitle = d.Title + " · " + from + " — " + to
	d.CoverNote = g.trf("Intervallo selezionato: %s — %s", from, to)
	d.Paragraph(g.trf("Intervallo selezionato: %s — %s", from, to))
	d.Paragraph(g.trf("Report inclusi: %d", len(periods)))
	d.Paragraph(g.tr("Sono inclusi i periodi interi che intersecano l'intervallo selezionato. I conteggi di periodi sovrapposti non devono essere sommati."))
	labels := map[period.Kind]string{period.Year: g.tr("Report annuali"), period.Month: g.tr("Report mensili"), period.Week: g.tr("Report settimanali"), period.Day: g.tr("Report giornalieri")}
	var previous period.Kind
	contentBytes := 0
	for _, p := range periods {
		if err := ctx.Err(); err != nil {
			return document.Document{}, err
		}
		part, err := g.Document(ctx, p)
		if err != nil {
			return document.Document{}, err
		}
		// Bound the retained presentation model, including chart series, before
		// allocating PNGs. No partial document is returned when the budget fails.
		encoded, err := json.Marshal(part.Blocks)
		if err != nil {
			return document.Document{}, err
		}
		contentBytes += len(encoded)
		if contentBytes > 32<<20 {
			return document.Document{}, fmt.Errorf("archive exceeds 32 MiB preparation limit; select fewer periods")
		}
		newGroup := previous != p.Kind
		if newGroup {
			d.Blocks = append(d.Blocks, document.Block{Text: labels[p.Kind], Level: 1, BreakBefore: true})
		}
		d.Blocks = append(d.Blocks, document.Block{Text: part.Title + " · " + p.Key, Level: 2, BreakBefore: !newGroup})
		for _, b := range part.Blocks {
			if b.Level > 0 {
				b.Level = 3
			}
			d.Blocks = append(d.Blocks, b)
		}
		previous = p.Kind
	}
	return d, nil
}

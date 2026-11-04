// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package exporter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/document"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/report"
)

// MaxArchiveReports bounds preparation before the renderer's page/byte limits.
const MaxArchiveReports = 200

var ErrArchiveLimit = errors.New("archive exceeds report limit; select fewer periods")

// ArchiveSelection selects whole calendar reports overlapping inclusive local
// dates. Empty dates select all observed days; nil Kinds selects enabled kinds.
// An explicitly empty Kinds slice is rejected rather than exporting everything.
type ArchiveSelection struct {
	From  string        `json:"from,omitempty"`
	To    string        `json:"to,omitempty"`
	Kinds []period.Kind `json:"kinds"`
}

// ArchivePlan is a cheap preview, recomputed in the export's own snapshot.
type ArchivePlan struct {
	From    string              `json:"from"`
	To      string              `json:"to"`
	Counts  map[period.Kind]int `json:"counts"`
	Total   int                 `json:"total"`
	Limit   int                 `json:"limit"`
	Allowed bool                `json:"allowed"`
}

var archiveOrder = []period.Kind{period.Year, period.Month, period.Week, period.Day}

func enabled(cfg *config.Config, k period.Kind) bool {
	switch k {
	case period.Year:
		return cfg.Reports.Yearly
	case period.Month:
		return cfg.Reports.Monthly
	case period.Week:
		return cfg.Reports.Weekly
	case period.Day:
		return cfg.Reports.Daily
	}
	return false
}

// Validate rejects ambiguous dates, disabled kinds and duplicate kinds.
func (s ArchiveSelection) Validate(cfg *config.Config) error {
	if (s.From == "") != (s.To == "") {
		return errors.New("archive requires both from and to dates")
	}
	if s.From != "" {
		from, e1 := period.Parse(period.Day, s.From, cfg.Location)
		to, e2 := period.Parse(period.Day, s.To, cfg.Location)
		if e1 != nil || e2 != nil || s.From != from.Key || s.To != to.Key || from.Start.After(to.Start) {
			return errors.New("archive requires ordered dates YYYY-MM-DD")
		}
	}
	seen := make(map[period.Kind]bool)
	if s.Kinds == nil {
		for _, k := range archiveOrder {
			if enabled(cfg, k) {
				seen[k] = true
			}
		}
	} else {
		for _, k := range s.Kinds {
			if seen[k] || !enabled(cfg, k) {
				return fmt.Errorf("invalid, duplicate or disabled archive kind %q", k)
			}
			seen[k] = true
		}
	}
	if len(seen) == 0 {
		return errors.New("select at least one enabled report kind")
	}
	return nil
}

func planArchive(ctx context.Context, cfg *config.Config, g *report.Generator, sel ArchiveSelection) (ArchivePlan, []period.Period, error) {
	plan := ArchivePlan{Counts: make(map[period.Kind]int), Limit: MaxArchiveReports}
	if err := sel.Validate(cfg); err != nil {
		return plan, nil, err
	}
	lo, hi, err := g.Q.DataRange()
	if err != nil {
		return plan, nil, err
	}
	if lo <= 0 {
		return plan, nil, errors.New("archive has no observed data")
	}
	first := period.Of(period.Day, time.Unix(lo, 0), cfg.Location)
	last := period.Of(period.Day, time.Unix(hi, 0), cfg.Location)
	if sel.From != "" {
		from, _ := period.Parse(period.Day, sel.From, cfg.Location)
		to, _ := period.Parse(period.Day, sel.To, cfg.Location)
		if from.Start.After(first.Start) {
			first = from
		}
		if to.Start.Before(last.Start) {
			last = to
		}
	}
	if first.Start.After(last.Start) {
		return plan, nil, errors.New("archive selection has no observed data")
	}
	plan.From, plan.To = first.Key, last.Key
	var periods []period.Period
	for _, k := range archiveOrder {
		selected := sel.Kinds == nil && enabled(cfg, k)
		for _, chosen := range sel.Kinds {
			selected = selected || k == chosen
		}
		if !selected {
			continue
		}
		for p := period.Of(k, first.Start, cfg.Location); p.Start.Before(last.End); p = p.Next() {
			if err := ctx.Err(); err != nil {
				return plan, nil, err
			}
			plan.Counts[k]++
			plan.Total++
			// Keep counting for the preview without retaining an unbounded list.
			if plan.Total <= MaxArchiveReports {
				periods = append(periods, p)
			}
		}
	}
	plan.Allowed = plan.Total > 0 && plan.Total <= MaxArchiveReports
	return plan, periods, nil
}

// PreviewArchive reads counts only. It does not render or allocate report models.
func PreviewArchive(ctx context.Context, cfg *config.Config, sel ArchiveSelection, version string) (ArchivePlan, error) {
	var plan ArchivePlan
	err := withGenerator(ctx, cfg, version, func(g *report.Generator) error {
		var err error
		plan, _, err = planArchive(ctx, cfg, g, sel)
		return err
	})
	return plan, err
}

// PrepareArchive materializes every selected report from one database snapshot.
func PrepareArchive(ctx context.Context, cfg *config.Config, sel ArchiveSelection, version string) (document.Document, error) {
	var d document.Document
	err := withGenerator(ctx, cfg, version, func(g *report.Generator) error {
		plan, periods, err := planArchive(ctx, cfg, g, sel)
		if err != nil {
			return err
		}
		if !plan.Allowed {
			return fmt.Errorf("%w: %d > %d", ErrArchiveLimit, plan.Total, plan.Limit)
		}
		d, err = g.ArchiveDocument(ctx, periods, plan.From, plan.To)
		return err
	})
	return d, err
}

// RenderArchive releases its database snapshot before charts and pagination.
func RenderArchive(ctx context.Context, cfg *config.Config, sel ArchiveSelection, format, version string) ([]byte, error) {
	d, err := PrepareArchive(ctx, cfg, sel, version)
	if err != nil {
		return nil, err
	}
	return document.Render(ctx, d, format)
}

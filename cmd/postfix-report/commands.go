// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/ingest"
	"github.com/fdefilippo/postfix-report/internal/norm"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/query"
	"github.com/fdefilippo/postfix-report/internal/report"
	"github.com/fdefilippo/postfix-report/internal/verify"
)

type cli struct {
	cfg    *config.Config
	log    *slog.Logger
	stdout io.Writer
	stderr io.Writer
}

type selection struct {
	all                     bool
	date, week, month, year string
}

func (c *cli) importCmd(ctx context.Context, a *app.App, files []string, dir string) int {
	paths := append([]string{}, files...)
	if dir != "" {
		d, err := ingest.DirFiles(dir)
		if err != nil {
			c.log.Error("cannot read directory", "dir", dir, "err", err)
			return exitError
		}
		paths = append(paths, d...)
	}
	sum, err := a.Import(ctx, "import", paths)
	if sum != nil {
		c.printImport(sum)
	}
	if err != nil {
		c.log.Error("import failed", "err", err)
		return exitError
	}
	if sum.FileErrors != nil {
		return exitPartial
	}
	return exitOK
}

func (c *cli) printImport(s *app.ImportSummary) {
	for _, f := range s.Files {
		if f.Skipped != "" {
			fmt.Fprintf(c.stdout, "%s: saltato (%s)\n", f.Path, f.Skipped)
			continue
		}
		fmt.Fprintf(c.stdout, "%s: %d righe, %d eventi nuovi, %d duplicati, %d non Postfix, %d non riconosciute\n",
			f.Path, f.Lines, f.Events, f.Duplicates, f.NonPostfix, f.Unknown)
	}
	fmt.Fprintf(c.stdout, "eventi correlati: %d (reiniezioni collegate %d, senza originale %d); giorni ricalcolati: %d\n",
		s.Correlated, s.Reinjected, s.Orphans, len(s.DaysUpdated))
	if len(s.FullHosts) > 0 {
		fmt.Fprintf(c.stdout, "host ricorrelati da zero (dati arretrati): %v\n", s.FullHosts)
	}
}

func (c *cli) generator(a *app.App) (*report.Generator, error) {
	key, err := a.AnonKey()
	if err != nil {
		return nil, err
	}
	g := &report.Generator{
		DB:      a.DB,
		Q:       c.query(a),
		Cfg:     c.cfg,
		Anon:    norm.NewAnonymizer(key, c.cfg.Privacy.AnonymizeEmail, c.cfg.Privacy.AnonymizeIP),
		Version: version,
	}
	return g, g.Init()
}

func (c *cli) query(a *app.App) *query.Q {
	return &query.Q{DB: a.DB, Loc: c.cfg.Location, Gap: int64(c.cfg.Coverage.GapThreshold.Seconds())}
}

func (c *cli) generateCmd(ctx context.Context, a *app.App, s *selection) int {
	g, err := c.generator(a)
	if err != nil {
		c.log.Error("generate", "err", err)
		return exitError
	}
	var res *report.Result
	n := 0
	for _, v := range []string{s.date, s.week, s.month, s.year} {
		if v != "" {
			n++
		}
	}
	if s.all {
		n++
	}
	if n > 1 {
		fmt.Fprintln(c.stderr, "generate: usare una sola tra --all, --date, --week, --month, --year")
		return exitUsage
	}
	sel := map[period.Kind]string{period.Day: s.date, period.Week: s.week, period.Month: s.month, period.Year: s.year}
	switch {
	case s.all:
		res, err = g.GenerateAll(ctx)
	case n == 1:
		for k, v := range sel {
			if v == "" {
				continue
			}
			p, perr := period.Parse(k, v, c.cfg.Location)
			if perr != nil {
				fmt.Fprintln(c.stderr, "generate:", perr)
				return exitUsage
			}
			res, err = g.GenerateOne(p)
		}
	default:
		res, err = g.GenerateDirty(ctx)
	}
	if res != nil {
		c.printGenerate(res)
	}
	if err != nil {
		c.log.Error("generate", "err", err)
		return exitError
	}
	return exitOK
}

func (c *cli) printGenerate(r *report.Result) {
	fmt.Fprintf(c.stdout, "pagine elaborate: %d, scritte: %d, invariate: %d (output: %s)\n",
		r.Rendered, r.Written, r.Unchanged, c.cfg.Output.Directory)
	c.log.Debug("written", "paths", r.Paths)
}

func (c *cli) updateCmd(ctx context.Context, a *app.App) int {
	paths := ingest.ExpandPaths(c.cfg.Input.Paths)
	sum, err := a.Import(ctx, "update", paths)
	if sum != nil {
		c.printImport(sum)
	}
	if err != nil {
		c.log.Error("update: import failed", "err", err)
		return exitError
	}
	g, err := c.generator(a)
	if err != nil {
		c.log.Error("update", "err", err)
		return exitError
	}
	res, err := g.GenerateDirty(ctx)
	if res != nil {
		c.printGenerate(res)
	}
	if err != nil {
		c.log.Error("update: generate failed", "err", err)
		return exitError
	}
	if sum.FileErrors != nil {
		return exitPartial
	}
	return exitOK
}

func (c *cli) rebuildCmd(ctx context.Context, a *app.App) int {
	cs, days, err := a.Rebuild(ctx)
	if err != nil {
		c.log.Error("rebuild", "err", err)
		return exitError
	}
	fmt.Fprintf(c.stdout, "eventi ricorrelati: %d (reiniezioni collegate %d, senza originale %d); giorni ricalcolati: %d\n",
		cs.Events, cs.Reinjected, cs.Orphans, len(days))
	g, err := c.generator(a)
	if err != nil {
		c.log.Error("rebuild", "err", err)
		return exitError
	}
	res, err := g.GenerateAll(ctx)
	if res != nil {
		c.printGenerate(res)
	}
	if err != nil {
		c.log.Error("rebuild: generate failed", "err", err)
		return exitError
	}
	return exitOK
}

func (c *cli) verifyCmd(ctx context.Context, a *app.App) int {
	r, err := verify.Run(a.DB, c.cfg.Location, c.cfg.Output.Directory)
	if err != nil {
		c.log.Error("verify", "err", err)
		return exitError
	}
	for _, ch := range r.Checks {
		mark := "OK  "
		switch {
		case !ch.OK && ch.Warning:
			mark = "WARN"
		case !ch.OK:
			mark = "FAIL"
		}
		fmt.Fprintf(c.stdout, "[%s] %s: %s\n", mark, ch.Name, ch.Detail)
	}
	if r.Failed() {
		return exitVerify
	}
	return exitOK
}

// statsOutput is the machine-readable output of the stats command.
type statsOutput struct {
	From      string             `json:"from"`
	To        string             `json:"to"`
	Coverage  float64            `json:"coverage"`
	Metrics   map[string]float64 `json:"metrics"`
	Delivery  query.DelayStats   `json:"delivery_delay"`
	Filter    query.DelayStats   `json:"filter_delay"`
	Anomalies query.Anomalies    `json:"anomalies"`
}

func (c *cli) statsCmd(ctx context.Context, a *app.App, from, to string, asJSON bool) int {
	q := c.query(a)
	lo, hi, err := q.DataRange()
	if err != nil {
		c.log.Error("stats", "err", err)
		return exitError
	}
	if lo == 0 {
		fmt.Fprintln(c.stderr, "stats: nessun dato importato")
		return exitError
	}
	if from == "" {
		from = period.DayKeyUnix(lo, c.cfg.Location)
	}
	if to == "" {
		to = period.DayKeyUnix(hi, c.cfg.Location)
	}
	pf, err1 := period.Parse(period.Day, from, c.cfg.Location)
	pt, err2 := period.Parse(period.Day, to, c.cfg.Location)
	if err1 != nil || err2 != nil || pt.Start.Before(pf.Start) {
		fmt.Fprintln(c.stderr, "stats: intervallo non valido")
		return exitUsage
	}
	var days []string
	for d := pf; !d.Start.After(pt.Start); d = d.Next() {
		days = append(days, d.Key)
	}
	out := statsOutput{From: from, To: to}
	if out.Metrics, err = q.Totals(days); err != nil {
		c.log.Error("stats", "err", err)
		return exitError
	}
	ivs, err := q.Intervals(pf.Start.Unix(), pt.End.Unix())
	if err != nil {
		c.log.Error("stats", "err", err)
		return exitError
	}
	var cov int64
	for _, iv := range ivs {
		s, e := max(iv.Start, pf.Start.Unix()), min(iv.End+1, pt.End.Unix())
		if e > s {
			cov += e - s
		}
	}
	out.Coverage = float64(cov) / float64(pt.End.Unix()-pf.Start.Unix())
	if out.Delivery, err = q.Delays(pf.Start.Unix(), pt.End.Unix(), "delivery"); err == nil {
		out.Filter, err = q.Delays(pf.Start.Unix(), pt.End.Unix(), "filter")
	}
	if err == nil {
		out.Anomalies, err = q.GetAnomalies()
	}
	if err != nil {
		c.log.Error("stats", "err", err)
		return exitError
	}
	if asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return exitOK
	}
	fmt.Fprintf(c.stdout, "Periodo %s – %s, copertura %.2f%%\n", from, to, out.Coverage*100)
	keys := make([]string, 0, len(out.Metrics))
	for k := range out.Metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(c.stdout, "  %-22s %16.2f\n", k, out.Metrics[k])
	}
	d := out.Delivery
	fmt.Fprintf(c.stdout, "Ritardo consegne: n=%d media=%.3fs p50=%.2f p90=%.2f p95=%.2f p99=%.2f max=%.2f\n",
		d.Count, d.Avg, d.P50, d.P90, d.P95, d.P99, d.Max)
	return exitOK
}

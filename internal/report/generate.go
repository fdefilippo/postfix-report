// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	ag "github.com/fdefilippo/postfix-report/internal/aggregate"
	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/norm"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/query"
	"github.com/fdefilippo/postfix-report/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed assets
var assetFS embed.FS

// generatedPlaceholder is replaced by the generation time when a page is
// written, so that content hashes ignore it.
const generatedPlaceholder = "@@GENERATED_AT@@"

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"barWidth": func(f float64) template.CSS { return template.CSS(fmt.Sprintf("%.1f%%", f*100)) },
}).ParseFS(templateFS, "templates/*.html"))

// Generator renders and writes report pages.
type Generator struct {
	DB      *store.DB
	Q       *query.Q
	Cfg     *config.Config
	Anon    *norm.Anonymizer
	Version string
	Now     func() time.Time

	hasData  bool
	firstDay period.Period
	lastDay  period.Period
	dataAsOf string
}

// Result reports what a generation run did.
type Result struct {
	Rendered  int
	Written   int
	Unchanged int
	Paths     []string // written pages
}

func (g *Generator) loc() *time.Location { return g.Cfg.Location }

func (g *Generator) enabled(k period.Kind) bool {
	switch k {
	case period.Day:
		return g.Cfg.Reports.Daily
	case period.Week:
		return g.Cfg.Reports.Weekly
	case period.Month:
		return g.Cfg.Reports.Monthly
	case period.Year:
		return g.Cfg.Reports.Yearly
	}
	return false
}

// inRange reports whether p overlaps the observed data range.
func (g *Generator) inRange(p period.Period) bool {
	return g.hasData && p.End.After(g.firstDay.Start) && p.Start.Before(g.lastDay.End)
}

// Init loads the data range; call before rendering.
func (g *Generator) Init() error {
	if g.Now == nil {
		g.Now = time.Now
	}
	lo, hi, err := g.Q.DataRange()
	if err != nil {
		return err
	}
	g.hasData = lo > 0
	if g.hasData {
		g.firstDay = period.Of(period.Day, time.Unix(lo, 0), g.loc())
		g.lastDay = period.Of(period.Day, time.Unix(hi, 0), g.loc())
		g.dataAsOf = g.ts(hi)
	}
	return nil
}

// AllPages lists every page of the archive.
func (g *Generator) AllPages() []period.Period {
	if !g.hasData {
		return nil
	}
	var out []period.Period
	for _, k := range period.Kinds {
		if !g.enabled(k) {
			continue
		}
		for p := period.Of(k, g.firstDay.Start, g.loc()); g.inRange(p); p = p.Next() {
			out = append(out, p)
		}
	}
	return out
}

// GenerateAll writes the assets, every period page and the index.
func (g *Generator) GenerateAll(ctx context.Context) (*Result, error) {
	res := &Result{}
	if err := g.writeAssets(res); err != nil {
		return res, err
	}
	for _, p := range g.AllPages() {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := g.writePeriod(p, res); err != nil {
			return res, err
		}
	}
	if err := g.writeIndex(res); err != nil {
		return res, err
	}
	_, err := g.DB.Exec(`DELETE FROM dirty_pages`)
	return res, err
}

// GenerateOne writes a single period page (and the assets).
func (g *Generator) GenerateOne(p period.Period) (*Result, error) {
	res := &Result{}
	if err := g.writeAssets(res); err != nil {
		return res, err
	}
	if !g.inRange(p) {
		return res, fmt.Errorf("%s %s: no data in this period (data range %s – %s)", p.Kind, p.Key, g.firstDay.Key, g.lastDay.Key)
	}
	if !g.enabled(p.Kind) {
		return res, fmt.Errorf("%s reports are disabled in the configuration", p.Kind)
	}
	err := g.writePeriod(p, res)
	if err == nil {
		_, err = g.DB.Exec(`DELETE FROM dirty_pages WHERE kind=? AND key=?`, string(p.Kind), p.Key)
	}
	return res, err
}

// GenerateDirty writes only the invalidated pages, then the index if anything
// changed or it was never generated.
func (g *Generator) GenerateDirty(ctx context.Context) (*Result, error) {
	res := &Result{}
	if err := g.writeAssets(res); err != nil {
		return res, err
	}
	rows, err := g.DB.Query(`SELECT kind, key FROM dirty_pages ORDER BY kind, key`)
	if err != nil {
		return res, err
	}
	var dirty [][2]string
	for rows.Next() {
		var k, key string
		if err := rows.Scan(&k, &key); err != nil {
			rows.Close()
			return res, err
		}
		dirty = append(dirty, [2]string{k, key})
	}
	rows.Close()
	index := false
	for _, d := range dirty {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if d[0] == "index" {
			index = true
		} else if p, err := period.Parse(period.Kind(d[0]), d[1], g.loc()); err == nil && g.inRange(p) && g.enabled(p.Kind) {
			if err := g.writePeriod(p, res); err != nil {
				return res, err
			}
		}
		if _, err := g.DB.Exec(`DELETE FROM dirty_pages WHERE kind=? AND key=?`, d[0], d[1]); err != nil {
			return res, err
		}
	}
	if !index {
		var n int
		g.DB.QueryRow(`SELECT COUNT(*) FROM report_pages WHERE path='index.html'`).Scan(&n)
		_, statErr := os.Stat(filepath.Join(g.Cfg.Output.Directory, "index.html"))
		index = n == 0 || statErr != nil
	}
	if index {
		if err := g.writeIndex(res); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (g *Generator) writePeriod(p period.Period, res *Result) error {
	pg, err := g.periodPage(p)
	if err != nil {
		return fmt.Errorf("%s %s: %w", p.Kind, p.Key, err)
	}
	return g.writePage(p.Path(), string(p.Kind), p.Key, pg, res)
}

func (g *Generator) writeIndex(res *Result) error {
	pg, err := g.indexPage()
	if err != nil {
		return fmt.Errorf("index: %w", err)
	}
	return g.writePage("index.html", "index", "index", pg, res)
}

// writePage renders a page and replaces the file atomically if its content
// (generation time excluded) changed or the file is missing.
func (g *Generator) writePage(rel, kind, key string, pg *Page, res *Result) error {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "page", pg); err != nil {
		return err
	}
	res.Rendered++
	sum := sha256.Sum256(buf.Bytes())
	hash := hex.EncodeToString(sum[:])
	dst := filepath.Join(g.Cfg.Output.Directory, filepath.FromSlash(rel))
	var old string
	err := g.DB.QueryRow(`SELECT hash FROM report_pages WHERE path=?`, rel).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if old == hash {
		if _, err := os.Stat(dst); err == nil {
			res.Unchanged++
			return nil
		}
	}
	now := g.Now()
	out := bytes.Replace(buf.Bytes(), []byte(generatedPlaceholder), []byte(now.In(g.loc()).Format("02/01/2006 15:04:05")), -1)
	if err := WriteAtomic(dst, out, 0o644); err != nil {
		return err
	}
	res.Written++
	res.Paths = append(res.Paths, rel)
	_, err = g.DB.Exec(`INSERT INTO report_pages(path, kind, key, hash, generated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET hash=excluded.hash, generated_at=excluded.generated_at`, rel, kind, key, hash, now.Unix())
	return err
}

func (g *Generator) writeAssets(res *Result) error {
	return fs.WalkDir(assetFS, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := assetFS.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(g.Cfg.Output.Directory, filepath.FromSlash(path))
		if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, data) {
			return nil
		}
		if err := WriteAtomic(dst, data, 0o644); err != nil {
			return err
		}
		res.Written++
		res.Paths = append(res.Paths, path)
		return nil
	})
}

// WriteAtomic writes data to a temporary file in the destination directory
// and renames it over path, so readers never see a partial file.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// ---- index ----

func (g *Generator) indexPage() (*Page, error) {
	pg := g.basePage("", "Panoramica", "index")
	// Only the index shows the end of the data: period pages must not change
	// when unrelated data arrives.
	pg.DataAsOf = g.dataAsOf
	if !g.hasData {
		pg.Banners = []Banner{{"warn", "Nessun dato importato. Eseguire 'postfix-report update' oppure 'postfix-report import'."}}
		return pg, nil
	}
	lo, hi, err := g.Q.DataRange()
	if err != nil {
		return nil, err
	}
	imp, err := g.Q.LastImport()
	if err != nil {
		return nil, err
	}
	pg.Subtitle = fmt.Sprintf("Dati dal %s al %s", g.ts(lo), g.ts(hi))
	if imp.ID > 0 {
		lvl := "ok"
		txt := fmt.Sprintf("Ultimo aggiornamento: %s (%s, esito %s; %s righe lette, %s eventi nuovi, %s duplicati ignorati).",
			g.ts(imp.Started), imp.Command, imp.Status, fmtInt(float64(imp.Lines)), fmtInt(float64(imp.Events)), fmtInt(float64(imp.Duplicates)))
		if imp.Status != "ok" {
			lvl = "bad"
			txt += " Errore: " + imp.Error
		}
		pg.Banners = append(pg.Banners, Banner{lvl, txt})
	}

	// Coverage summary over the whole range.
	all := period.Period{Kind: period.Day, Start: g.firstDay.Start, End: g.lastDay.End}
	ivs, err := g.Q.Intervals(all.Start.Unix(), all.End.Unix())
	if err != nil {
		return nil, err
	}
	var days []query.Bucket
	var full, part, miss int
	for d := g.firstDay; !d.Start.After(g.lastDay.Start); d = d.Next() {
		b := coverageOf(ivs, d.Start.Unix(), d.End.Unix())
		b.Key = d.Key
		days = append(days, b)
		switch b.Status {
		case query.Complete:
			full++
		case query.Partial:
			part++
		default:
			miss++
		}
	}
	keys := make([]string, len(days))
	for i, d := range days {
		keys[i] = d.Key
	}
	totals, err := g.Q.Totals(keys)
	if err != nil {
		return nil, err
	}
	pg.Blocks = append(pg.Blocks, section("Totali dell'archivio",
		fmt.Sprintf("Periodo coperto: %d giorni (%d completi, %d parziali, %d senza dati).", len(days), full, part, miss),
		g.kpis(totals, nil, false)))

	// Last seven days with data.
	var last7 []query.Bucket
	for i := len(days) - 1; i >= 0 && len(last7) < 7; i-- {
		if days[i].Status != query.Missing {
			last7 = append([]query.Bucket{days[i]}, last7...)
		}
	}
	byDay, err := g.Q.ByDay(last7[0].Key, last7[len(last7)-1].Key)
	if err != nil {
		return nil, err
	}
	vals := make([]query.Metrics, len(last7))
	labels, short := make([]string, len(last7)), make([]string, len(last7))
	links := make([]string, len(last7))
	for i, b := range last7 {
		vals[i] = byDay[b.Key]
		if vals[i] == nil {
			vals[i] = query.Metrics{}
		}
		d, _ := period.Parse(period.Day, b.Key, g.loc())
		labels[i] = g.title(d)
		short[i] = d.Start.Format("02/01")
		links[i] = g.href("", d)
		b.Start, b.End = d.Start.Unix(), d.End.Unix()
	}
	pg.Blocks = append(pg.Blocks, section("Traffico degli ultimi sette giorni disponibili",
		"Giorni con almeno una riga di log; clic su un giorno per il dettaglio.",
		card("", "", g.bucketChart("last7", "Ultimi sette giorni", "bar", "", last7, labels, short, links, vals, trafficSeries))))

	// Latest reports.
	latest := &Table{ID: "latest", Head: []Th{{Text: "Report"}, {Text: "Periodo"}, {Text: "Copertura"}}}
	names := map[period.Kind]string{period.Day: "Giornaliero", period.Week: "Settimanale", period.Month: "Mensile", period.Year: "Annuale"}
	for _, k := range period.Kinds {
		if !g.enabled(k) {
			continue
		}
		for i, p := 0, period.Of(k, g.lastDay.Start, g.loc()); i < 2 && g.inRange(p); i, p = i+1, p.Prev() {
			b := coverageOf(ivs, p.Start.Unix(), p.End.Unix())
			latest.Rows = append(latest.Rows, Tr{Class: b.Status, Cells: []Td{{Text: names[k]},
				{Text: g.title(p), Href: g.href("", p)}, {Text: statusText(b), Badge: badgeClass(b.Status)}}})
		}
	}
	srcBlock, err := g.sourcesCard(imp)
	if err != nil {
		return nil, err
	}
	pg.Blocks = append(pg.Blocks, section("Report recenti e acquisizioni", "",
		grid(card("Ultimi report", "", Block{Kind: "table", Table: latest}), srcBlock)))

	arch, err := g.archive(ivs)
	if err != nil {
		return nil, err
	}
	pg.Blocks = append(pg.Blocks, section("Archivio", "Mesi e anni disponibili; il numero indica i messaggi accettati.",
		Block{Kind: "archive", Archive: arch}))

	an, err := g.anomaliesCard()
	if err != nil {
		return nil, err
	}
	pg.Blocks = append(pg.Blocks, section("Qualità dei dati", "", grid(an, g.coverageDaysCard(days))))
	return pg, nil
}

func (g *Generator) sourcesCard(imp query.ImportInfo) (Block, error) {
	srcs, err := g.Q.Sources()
	if err != nil {
		return Block{}, err
	}
	tbl := &Table{ID: "sources", Sortable: true, Head: []Th{{Text: "Sorgente"}, {Text: "Righe", Num: true}, {Text: "Dal"}, {Text: "Al"}, {Text: "Stato"}}}
	for _, s := range srcs {
		st := "in crescita (letta fino alla fine)"
		if s.Compressed {
			st = "compressa"
			if s.Complete {
				st += ", letta interamente"
			}
		}
		tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: filepath.Base(s.Path), Title: s.Path, Key: true},
			{Text: fmtInt(float64(s.Lines)), Num: true, Sort: fmt.Sprint(s.Lines)},
			{Text: g.ts(s.FirstTS), Sort: fmt.Sprint(s.FirstTS)}, {Text: g.ts(s.LastTS), Sort: fmt.Sprint(s.LastTS)}, {Text: st}}})
	}
	return card("Stato delle acquisizioni", "Le sorgenti sono riconosciute dal contenuto: un file ruotato non viene reimportato.",
		Block{Kind: "table", Table: tbl}), nil
}

func (g *Generator) archive(ivs []query.Interval) ([]ArchiveYear, error) {
	var out []ArchiveYear
	for y := period.Of(period.Year, g.firstDay.Start, g.loc()); g.inRange(y); y = y.Next() {
		t, err := g.Q.Totals(y.Days())
		if err != nil {
			return nil, err
		}
		ay := ArchiveYear{Year: y.Key, Href: g.href("", y), Total: fmtInt(t.Get(ag.MAccepted)) + " messaggi"}
		byDay, err := g.Q.ByDay(y.Days()[0], y.Days()[len(y.Days())-1])
		if err != nil {
			return nil, err
		}
		for m := period.Of(period.Month, y.Start, g.loc()); m.Start.Before(y.End); m = m.Next() {
			am := ArchiveMonth{Label: monthShort[m.Start.Month()-1]}
			if g.inRange(m) {
				b := coverageOf(ivs, m.Start.Unix(), m.End.Unix())
				am.Href, am.Status = g.href("", m), b.Status
				var acc float64
				for _, d := range m.Days() {
					acc += byDay[d].Get(ag.MAccepted)
				}
				am.Value = fmtInt(acc)
				if b.Status == query.Missing {
					am.Value = "n/d"
				}
			}
			ay.Months = append(ay.Months, am)
		}
		out = append(out, ay)
	}
	return out, nil
}

var flagText = map[string]string{
	"start_not_observed":           "Istanze il cui inizio non è nei log (già in coda prima dei dati)",
	"qid_reused_before_removed":    "Queue ID riutilizzati senza 'removed' osservato",
	"reinject_without_original":    "Reiniezioni del filtro senza messaggio originale",
	"expired_without_pending_rcpt": "Scadenze senza destinatari pendenti osservati",
	"hold":                         "Messaggi messi in hold",
	"stale":                        "Istanze chiuse per inattività (stale_after)",
	"delivery_before_active":       "Consegne senza 'queue active' osservato",
}

func (g *Generator) anomaliesCard() (Block, error) {
	an, err := g.Q.GetAnomalies()
	if err != nil {
		return Block{}, err
	}
	tbl := &Table{ID: "anomalies", Head: []Th{{Text: "Condizione"}, {Text: "Conteggio", Num: true}}}
	add := func(t string, v int64) {
		tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: t}, {Text: fmtInt(float64(v)), Num: true}}})
	}
	add("Istanze di coda ancora aperte alla fine dei dati (messaggi in elaborazione)", an.OpenMessages)
	add("Destinatari senza esito definitivo", an.PendingRcpts)
	add("Messaggi passati al filtro senza reiniezione osservata", an.FilteredNoLink)
	add("Righe Postfix non riconosciute", an.UnknownLines)
	for f, n := range an.Flags {
		t := flagText[f]
		if t == "" {
			t = f
		}
		add(t, n)
	}
	add("Interruzioni nella copertura (buchi > soglia)", int64(len(an.Gaps)))
	note := "Condizioni che limitano l'interpretazione; non sono attribuite ad alcun esito."
	if len(an.Gaps) > 0 {
		var gs []string
		for i, gp := range an.Gaps {
			if i == 5 {
				gs = append(gs, "…")
				break
			}
			gs = append(gs, g.ts(gp.Start)+" → "+g.ts(gp.End))
		}
		note += " Buchi: " + strings.Join(gs, "; ") + "."
	}
	return card("Anomalie e limiti", note, Block{Kind: "table", Table: tbl}), nil
}

func (g *Generator) coverageDaysCard(days []query.Bucket) Block {
	tbl := &Table{ID: "daycoverage", Sortable: true, Filter: len(days) > 10, Head: []Th{{Text: "Giorno"}, {Text: "Copertura"}, {Text: "Osservato", Num: true}}}
	for i := len(days) - 1; i >= 0 && len(tbl.Rows) < 62; i-- {
		b := days[i]
		d, _ := period.Parse(period.Day, b.Key, g.loc())
		tbl.Rows = append(tbl.Rows, Tr{Class: b.Status, Cells: []Td{{Text: g.title(d), Href: g.href("", d), Sort: b.Key},
			{Text: statusText(b), Badge: badgeClass(b.Status)}, {Text: fmtPct(b.Fraction * 100), Num: true, Sort: fmt.Sprint(b.Fraction)}}})
	}
	return card("Giorni acquisiti", "Ultimi giorni dell'archivio. Un giorno è completo solo se tutte le sue ore sono state osservate.",
		Block{Kind: "table", Table: tbl})
}

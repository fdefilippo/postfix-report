// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package verify checks the integrity of the database, the consistency of
// correlation and aggregates, and the integrity of the generated HTML.
package verify

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fdefilippo/postfix-report/internal/aggregate"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
)

// Check is the outcome of one verification.
type Check struct {
	Name    string
	OK      bool
	Warning bool // not an error: limits interpretation
	Detail  string
}

// Report collects all checks.
type Report struct {
	Checks []Check
}

// Failed reports whether any check failed (warnings excluded).
func (r *Report) Failed() bool {
	for _, c := range r.Checks {
		if !c.OK && !c.Warning {
			return true
		}
	}
	return false
}

func (r *Report) add(name string, ok bool, warn bool, f string, a ...any) {
	r.Checks = append(r.Checks, Check{Name: name, OK: ok, Warning: warn && !ok, Detail: fmt.Sprintf(f, a...)})
}

// Run performs all checks. outDir may be "" to skip HTML checks.
func Run(db *store.DB, loc *time.Location, outDir string) (*Report, error) {
	r := &Report{}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return nil, err
	}
	r.add("sqlite integrity_check", integrity == "ok", false, "%s", integrity)
	fk, err := countRows(db, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, err
	}
	r.add("foreign keys", fk == 0, false, "%d violazioni", fk)
	v, err := db.SchemaVersion()
	if err != nil {
		return nil, err
	}
	r.add("schema", v > 0, false, "versione %d", v)

	// Correlation completeness: every stored event up to the watermark has
	// been turned into its derived row.
	var pending int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM events e LEFT JOIN corr_state c ON c.host = e.host
		WHERE c.host IS NULL OR e.ts > c.wm_ts OR (e.ts = c.wm_ts AND e.id > c.wm_id)`).Scan(&pending); err != nil {
		return nil, err
	}
	r.add("eventi correlati", pending == 0, false, "%d eventi non ancora correlati", pending)
	pairs := []struct{ name, events, derived string }{
		{"righe di consegna → deliveries", `SELECT COUNT(*) FROM events WHERE kind='delivery'`, `SELECT COUNT(*) FROM deliveries`},
		{"rifiuti/discard → rejects", `SELECT COUNT(*) FROM events WHERE kind IN ('reject','discard')`, `SELECT COUNT(*) FROM rejects`},
		{"eventi di sicurezza", `SELECT COUNT(*) FROM events WHERE kind IN ('sasl_fail','tls_error','session_abort')`, `SELECT COUNT(*) FROM security_events`},
		{"istanze smtpd (client=) → istanze smtp", `SELECT COUNT(*) FROM events WHERE kind='client'`, `SELECT COUNT(*) FROM instances WHERE origin='smtp'`},
		{"pickup → istanze local+reinject", `SELECT COUNT(*) FROM events WHERE kind='pickup'`, `SELECT COUNT(*) FROM instances WHERE origin IN ('local','reinject')`},
		{"notifiche bounce → istanze notification", `SELECT COUNT(*) FROM events WHERE kind='notification'`, `SELECT COUNT(*) FROM instances WHERE origin='notification'`},
	}
	for _, p := range pairs {
		var a, b int64
		if err := db.QueryRow(p.events).Scan(&a); err != nil {
			return nil, err
		}
		if err := db.QueryRow(p.derived).Scan(&b); err != nil {
			return nil, err
		}
		r.add(p.name, a == b, false, "eventi %d, righe derivate %d", a, b)
	}
	var multi int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT parent_id FROM instances WHERE parent_id IS NOT NULL
		GROUP BY parent_id HAVING COUNT(*) > 1)`).Scan(&multi); err != nil {
		return nil, err
	}
	r.add("una sola reiniezione per messaggio originale", multi == 0, false, "%d originali con più reiniezioni", multi)
	var orphans, noLink int64
	db.QueryRow(`SELECT COUNT(*) FROM instances WHERE origin='reinject' AND parent_id IS NULL`).Scan(&orphans)
	db.QueryRow(`SELECT COUNT(*) FROM instances i WHERE filtered=1 AND NOT EXISTS (SELECT 1 FROM instances c WHERE c.parent_id=i.id)`).Scan(&noLink)
	r.add("reiniezioni senza originale", orphans == 0, true, "%d", orphans)
	r.add("passaggi al filtro senza reiniezione", noLink == 0, true, "%d", noLink)
	var unknown int64
	db.QueryRow(`SELECT COALESCE(SUM(count),0) FROM unknown_lines`).Scan(&unknown)
	r.add("righe Postfix non riconosciute", unknown == 0, true, "%d", unknown)

	// Aggregates: recompute every day in memory and compare.
	agg := &aggregate.Aggregator{DB: db, Loc: loc}
	days, err := listDays(db, loc)
	if err != nil {
		return nil, err
	}
	var diffs []string
	for _, d := range days {
		dd, err := agg.Compare(d)
		if err != nil {
			return nil, err
		}
		diffs = append(diffs, dd...)
	}
	detail := fmt.Sprintf("%d giorni verificati", len(days))
	if len(diffs) > 0 {
		detail += "; differenze: " + strings.Join(first(diffs, 10), " | ")
	}
	r.add("aggregati ricalcolati = aggregati memorizzati", len(diffs) == 0, false, "%s", detail)
	var dirtyDays, dirtyPages int64
	db.QueryRow(`SELECT COUNT(*) FROM dirty_days`).Scan(&dirtyDays)
	db.QueryRow(`SELECT COUNT(*) FROM dirty_pages`).Scan(&dirtyPages)
	r.add("aggregati in attesa di ricalcolo", dirtyDays == 0, false, "%d giorni", dirtyDays)
	r.add("report in attesa di rigenerazione", dirtyPages == 0, true, "%d pagine (eseguire 'generate')", dirtyPages)

	if outDir != "" {
		if _, err := os.Stat(outDir); os.IsNotExist(err) {
			r.add("directory dei report", false, true, "%s non esiste: report non ancora generati", outDir)
			return r, nil
		}
		if err := checkHTML(db, outDir, r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func first(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("… altre %d", len(s)-n))
	}
	return s
}

func countRows(db *store.DB, q string) (int, error) {
	rows, err := db.Query(q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// listDays returns every local day of the observed range plus any day with
// stored aggregates: a day with data but no aggregates is an inconsistency
// that Compare detects.
func listDays(db *store.DB, loc *time.Location) ([]string, error) {
	seen := map[string]bool{}
	var lo, hi sql.NullInt64
	if err := db.QueryRow(`SELECT MIN(start_ts), MAX(end_ts) FROM segments`).Scan(&lo, &hi); err != nil {
		return nil, err
	}
	if lo.Valid {
		for d := period.Of(period.Day, time.Unix(lo.Int64, 0), loc); d.Start.Unix() <= hi.Int64; d = d.Next() {
			seen[d.Key] = true
		}
	}
	rows, err := db.Query(`SELECT DISTINCT day FROM agg_hour`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		seen[d] = true
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, rows.Err()
}

var reRef = regexp.MustCompile(`(?:href|src)="([^"]*)"`)

// checkHTML verifies that recorded pages exist, that no temporary files are
// left behind, that every relative link resolves to an existing file and
// that no page references an external resource.
func checkHTML(db *store.DB, outDir string, r *Report) error {
	rows, err := db.Query(`SELECT path FROM report_pages`)
	if err != nil {
		return err
	}
	var missing []string
	n := 0
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		n++
		if _, err := os.Stat(filepath.Join(outDir, filepath.FromSlash(p))); err != nil {
			missing = append(missing, p)
		}
	}
	rows.Close()
	r.add("pagine registrate presenti su disco", len(missing) == 0, false, "%d pagine, mancanti: %s", n, strings.Join(first(missing, 5), ", "))
	res, err := CheckLinks(outDir)
	if err != nil {
		return err
	}
	r.add("collegamenti relativi validi", len(res.Broken) == 0, false, "%d file HTML, %d link, rotti: %s",
		res.Files, res.Links, strings.Join(first(res.Broken, 5), ", "))
	r.add("nessuna risorsa esterna", len(res.External) == 0, false, "%s", strings.Join(first(res.External, 5), ", "))
	r.add("nessun file temporaneo residuo", len(res.Temp) == 0, true, "%s", strings.Join(first(res.Temp, 5), ", "))
	r.add("solo risorse di consultazione nella directory web", len(res.Foreign) == 0, true, "file inattesi: %s",
		strings.Join(first(res.Foreign, 5), ", "))
	return nil
}

// LinkReport is the result of CheckLinks.
type LinkReport struct {
	Files    int
	Links    int
	Broken   []string
	External []string
	Temp     []string
	Foreign  []string
}

// CheckLinks scans the HTML files under dir.
func CheckLinks(dir string) (*LinkReport, error) {
	lr := &LinkReport{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		base := d.Name()
		if strings.HasPrefix(base, ".tmp-") {
			lr.Temp = append(lr.Temp, rel)
			return nil
		}
		ext := strings.ToLower(filepath.Ext(base))
		switch ext {
		case ".html", ".css", ".js", ".woff2", ".woff", ".svg", ".png", ".ico", ".txt":
		default:
			lr.Foreign = append(lr.Foreign, rel)
		}
		if ext != ".html" {
			return nil
		}
		lr.Files++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range reRef.FindAllStringSubmatch(string(data), -1) {
			ref := m[1]
			lr.Links++
			u, err := url.Parse(ref)
			if err != nil {
				lr.Broken = append(lr.Broken, rel+" → "+ref)
				continue
			}
			if u.Scheme != "" || strings.HasPrefix(ref, "//") {
				lr.External = append(lr.External, rel+" → "+ref)
				continue
			}
			if u.Path == "" || strings.HasPrefix(u.Path, "/") {
				if strings.HasPrefix(u.Path, "/") {
					lr.Broken = append(lr.Broken, rel+" → "+ref+" (percorso assoluto)")
				}
				continue
			}
			target := filepath.Join(filepath.Dir(path), filepath.FromSlash(u.Path))
			if _, err := os.Stat(target); err != nil {
				lr.Broken = append(lr.Broken, rel+" → "+ref)
			}
		}
		return nil
	})
	return lr, err
}

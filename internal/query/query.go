// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package query reads aggregates, coverage and exact delay statistics for a
// period. It is the only source of numbers for the reports and the stats
// command.
package query

import (
	"database/sql"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
)

// Coverage states.
const (
	Complete = "complete"
	Partial  = "partial"
	Missing  = "missing"
)

// Q runs queries.
type Q struct {
	DB  *store.DB
	Loc *time.Location
	// Gap is the maximum distance (seconds) between two observed lines for
	// the time between them to count as observed.
	Gap int64
}

// Interval is an observed time interval [Start, End] (Unix seconds).
type Interval struct{ Start, End int64 }

// Intervals returns the merged observed intervals overlapping [lo, hi).
func (q *Q) Intervals(lo, hi int64) ([]Interval, error) {
	rows, err := q.DB.Query(`SELECT start_ts, end_ts FROM segments WHERE end_ts >= ? AND start_ts < ? ORDER BY start_ts`,
		lo-q.Gap, hi+q.Gap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interval
	for rows.Next() {
		var iv Interval
		if err := rows.Scan(&iv.Start, &iv.End); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && iv.Start-out[n-1].End <= q.Gap {
			if iv.End > out[n-1].End {
				out[n-1].End = iv.End
			}
			continue
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

// covered returns the number of seconds of [lo, hi) inside the intervals.
// An interval [s, e] of line timestamps covers [s, e+1).
func covered(ivs []Interval, lo, hi int64) int64 {
	var n int64
	for _, iv := range ivs {
		s, e := max(iv.Start, lo), min(iv.End+1, hi)
		if e > s {
			n += e - s
		}
	}
	return n
}

// Bucket is a time bucket with its coverage.
type Bucket struct {
	Key      string // hour "00".."23", day key, month key
	Label    string
	Start    int64
	End      int64
	Covered  int64 // seconds observed
	Status   string
	Fraction float64
}

func status(cov, total int64) (string, float64) {
	if total <= 0 {
		return Missing, 0
	}
	f := float64(cov) / float64(total)
	switch {
	case cov >= total:
		return Complete, 1
	case cov <= 0:
		return Missing, 0
	default:
		return Partial, f
	}
}

func (q *Q) bucket(ivs []Interval, key, label string, lo, hi int64) Bucket {
	c := covered(ivs, lo, hi)
	st, f := status(c, hi-lo)
	return Bucket{Key: key, Label: label, Start: lo, End: hi, Covered: c, Status: st, Fraction: f}
}

// HourBuckets returns the 24 local hours of a day. On DST days an hour may be
// 2 h long (fall back) or absent (spring forward: 0 s, reported missing).
func (q *Q) HourBuckets(day period.Period) ([]Bucket, error) {
	ivs, err := q.Intervals(day.Start.Unix(), day.End.Unix())
	if err != nil {
		return nil, err
	}
	var out []Bucket
	for h := 0; h < 24; h++ {
		lo, hi := hourBounds(day, h, q.Loc)
		b := q.bucket(ivs, twoDigits(h), twoDigits(h), lo, hi)
		if hi <= lo {
			b.Status = "nonexistent"
		}
		out = append(out, b)
	}
	return out, nil
}

// hourBounds returns the Unix range of local hour h of day: all instants
// whose local hour is h.
func hourBounds(day period.Period, h int, loc *time.Location) (int64, int64) {
	start, end := day.Start.Unix(), day.End.Unix()
	lo, hi := int64(-1), int64(-1)
	// Scan in 15-minute steps: offsets change on quarter-hour boundaries.
	for t := start; t < end; t += 900 {
		if time.Unix(t, 0).In(loc).Hour() == h {
			if lo < 0 {
				lo = t
			}
			hi = t + 900
		}
	}
	if lo < 0 {
		return 0, 0
	}
	return lo, hi
}

func twoDigits(h int) string { return string([]byte{byte('0' + h/10), byte('0' + h%10)}) }

// DayBuckets returns one bucket per day of the period.
func (q *Q) DayBuckets(p period.Period) ([]Bucket, error) {
	ivs, err := q.Intervals(p.Start.Unix(), p.End.Unix())
	if err != nil {
		return nil, err
	}
	var out []Bucket
	for _, d := range p.Days() {
		dp, _ := period.Parse(period.Day, d, q.Loc)
		out = append(out, q.bucket(ivs, d, d, dp.Start.Unix(), dp.End.Unix()))
	}
	return out, nil
}

// MonthBuckets returns one bucket per month of a year.
func (q *Q) MonthBuckets(y period.Period) ([]Bucket, error) {
	ivs, err := q.Intervals(y.Start.Unix(), y.End.Unix())
	if err != nil {
		return nil, err
	}
	var out []Bucket
	for m := period.Of(period.Month, y.Start, q.Loc); m.Start.Before(y.End); m = m.Next() {
		out = append(out, q.bucket(ivs, m.Key, m.Key, m.Start.Unix(), m.End.Unix()))
	}
	return out, nil
}

// Metrics is a set of metric values.
type Metrics map[string]float64

// Get returns a metric (0 if absent).
func (m Metrics) Get(k string) float64 { return m[k] }

// Totals sums the hourly aggregates of the given days.
func (q *Q) Totals(days []string) (Metrics, error) {
	out := Metrics{}
	if len(days) == 0 {
		return out, nil
	}
	rows, err := q.DB.Query(`SELECT metric, SUM(value) FROM agg_hour WHERE day >= ? AND day <= ? GROUP BY metric`,
		days[0], days[len(days)-1])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v float64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ByHour returns the hourly metrics of a day (index = local hour).
func (q *Q) ByHour(days []string) ([24]Metrics, error) {
	var out [24]Metrics
	for i := range out {
		out[i] = Metrics{}
	}
	if len(days) == 0 {
		return out, nil
	}
	rows, err := q.DB.Query(`SELECT hour, metric, SUM(value) FROM agg_hour WHERE day >= ? AND day <= ? GROUP BY hour, metric`,
		days[0], days[len(days)-1])
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var h int
		var k string
		var v float64
		if err := rows.Scan(&h, &k, &v); err != nil {
			return out, err
		}
		if h >= 0 && h < 24 {
			out[h][k] = v
		}
	}
	return out, rows.Err()
}

// ByDay returns the metrics per day key for the inclusive day range.
func (q *Q) ByDay(first, last string) (map[string]Metrics, error) {
	out := map[string]Metrics{}
	rows, err := q.DB.Query(`SELECT day, metric, SUM(value) FROM agg_hour WHERE day >= ? AND day <= ? GROUP BY day, metric`, first, last)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d, k string
		var v float64
		if err := rows.Scan(&d, &k, &v); err != nil {
			return nil, err
		}
		if out[d] == nil {
			out[d] = Metrics{}
		}
		out[d][k] = v
	}
	return out, rows.Err()
}

// RankItem is one ranking row.
type RankItem struct {
	Key   string
	Value int64
}

// Ranking returns the top entries of a dimension over the inclusive day
// range, plus the total over all keys and the number of distinct keys.
func (q *Q) Ranking(first, last, dim string, limit int) ([]RankItem, int64, int, error) {
	rows, err := q.DB.Query(`SELECT key, SUM(value) AS v FROM agg_dim WHERE day >= ? AND day <= ? AND dim = ?
		GROUP BY key ORDER BY v DESC, key`, first, last, dim)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var out []RankItem
	var total int64
	n := 0
	for rows.Next() {
		var it RankItem
		if err := rows.Scan(&it.Key, &it.Value); err != nil {
			return nil, 0, 0, err
		}
		total += it.Value
		n++
		if len(out) < limit {
			out = append(out, it)
		}
	}
	return out, total, n, rows.Err()
}

// DelayStats are exact statistics of delivery delays (seconds).
type DelayStats struct {
	Count                        int
	Avg, P50, P90, P95, P99, Max float64
}

// Delays computes exact delay statistics of successful deliveries in [lo, hi)
// (nearest-rank percentiles).
func (q *Q) Delays(lo, hi int64, class string) (DelayStats, error) {
	rows, err := q.DB.Query(`SELECT delay FROM deliveries WHERE ts >= ? AND ts < ? AND class = ? AND status = 'sent'
		AND agent != 'discard' AND delay IS NOT NULL ORDER BY delay`, lo, hi, class)
	if err != nil {
		return DelayStats{}, err
	}
	defer rows.Close()
	var v []float64
	var sum float64
	for rows.Next() {
		var d float64
		if err := rows.Scan(&d); err != nil {
			return DelayStats{}, err
		}
		v = append(v, d)
		sum += d
	}
	if err := rows.Err(); err != nil {
		return DelayStats{}, err
	}
	return delayStats(v, sum), nil
}

func delayStats(sorted []float64, sum float64) DelayStats {
	n := len(sorted)
	if n == 0 {
		return DelayStats{}
	}
	if !sort.Float64sAreSorted(sorted) {
		sort.Float64s(sorted)
	}
	rank := func(p float64) float64 {
		i := int(math.Ceil(p/100*float64(n))) - 1
		if i < 0 {
			i = 0
		}
		return sorted[i]
	}
	return DelayStats{Count: n, Avg: sum / float64(n), P50: rank(50), P90: rank(90), P95: rank(95), P99: rank(99), Max: sorted[n-1]}
}

// DataRange returns the first and last observed timestamps (0 if no data).
func (q *Q) DataRange() (int64, int64, error) {
	var lo, hi sql.NullInt64
	err := q.DB.QueryRow(`SELECT MIN(start_ts), MAX(end_ts) FROM segments`).Scan(&lo, &hi)
	return lo.Int64, hi.Int64, err
}

// ImportInfo describes the latest import run.
type ImportInfo struct {
	ID         int64
	Command    string
	Started    int64
	Finished   int64
	Status     string
	Files      int64
	Lines      int64
	Events     int64
	Duplicates int64
	Error      string
}

// LastImport returns the latest import run (ID 0 if none).
func (q *Q) LastImport() (ImportInfo, error) {
	var ii ImportInfo
	var fin sql.NullInt64
	var errS sql.NullString
	err := q.DB.QueryRow(`SELECT id, command, started_at, finished_at, status, files, lines, events, duplicates, error
		FROM imports ORDER BY id DESC LIMIT 1`).Scan(&ii.ID, &ii.Command, &ii.Started, &fin, &ii.Status, &ii.Files,
		&ii.Lines, &ii.Events, &ii.Duplicates, &errS)
	if err == sql.ErrNoRows {
		return ii, nil
	}
	ii.Finished, ii.Error = fin.Int64, errS.String
	return ii, err
}

// SourceInfo describes an imported log source.
type SourceInfo struct {
	Path       string
	Lines      int64
	FirstTS    int64
	LastTS     int64
	Compressed bool
	Complete   bool
	Updated    int64
}

// Sources lists the known log sources ordered by first timestamp.
func (q *Q) Sources() ([]SourceInfo, error) {
	rows, err := q.DB.Query(`SELECT path, lines, COALESCE(first_ts,0), COALESCE(last_ts,0), compressed, complete, updated_at
		FROM sources ORDER BY first_ts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceInfo
	for rows.Next() {
		var s SourceInfo
		if err := rows.Scan(&s.Path, &s.Lines, &s.FirstTS, &s.LastTS, &s.Compressed, &s.Complete, &s.Updated); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Anomalies counts conditions that limit the interpretation of the data.
type Anomalies struct {
	Flags          map[string]int64 // instance flags
	UnknownLines   int64
	FilteredNoLink int64 // handed to the filter without an observed reinjection
	OpenMessages   int64 // accepted messages without a final outcome yet
	PendingRcpts   int64
	Gaps           []Interval // unobserved intervals between the first and last line
}

// GetAnomalies computes the anomaly summary.
func (q *Q) GetAnomalies() (Anomalies, error) {
	an := Anomalies{Flags: map[string]int64{}}
	rows, err := q.DB.Query(`SELECT flags, COUNT(*) FROM instances WHERE flags != '' GROUP BY flags`)
	if err != nil {
		return an, err
	}
	for rows.Next() {
		var f string
		var n int64
		if err := rows.Scan(&f, &n); err != nil {
			rows.Close()
			return an, err
		}
		for _, x := range strings.Split(f, ",") {
			an.Flags[x] += n
		}
	}
	rows.Close()
	if err := q.DB.QueryRow(`SELECT COALESCE(SUM(count),0) FROM unknown_lines`).Scan(&an.UnknownLines); err != nil {
		return an, err
	}
	if err := q.DB.QueryRow(`SELECT COUNT(*) FROM instances i WHERE filtered=1 AND NOT EXISTS
		(SELECT 1 FROM instances c WHERE c.parent_id = i.id)`).Scan(&an.FilteredNoLink); err != nil {
		return an, err
	}
	if err := q.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE state='open' AND accepted_ts IS NOT NULL
		AND origin != 'probe'`).Scan(&an.OpenMessages); err != nil {
		return an, err
	}
	if err := q.DB.QueryRow(`SELECT COUNT(*) FROM recipients WHERE status='pending'`).Scan(&an.PendingRcpts); err != nil {
		return an, err
	}
	lo, hi, err := q.DataRange()
	if err != nil || lo == 0 {
		return an, err
	}
	ivs, err := q.Intervals(lo, hi+1)
	if err != nil {
		return an, err
	}
	for i := 1; i < len(ivs); i++ {
		an.Gaps = append(an.Gaps, Interval{ivs[i-1].End, ivs[i].Start})
	}
	return an, nil
}

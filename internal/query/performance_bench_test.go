// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package query

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// These isolated fixtures reproduce the production agg_dim layout and delay
// query/index, but do not claim whole-pipeline or native production coverage.
func benchmarkDB(b *testing.B) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite", filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Error(err)
		}
	})
	for _, s := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL"} {
		if _, err := db.Exec(s); err != nil {
			b.Fatal(err)
		}
	}
	return db
}

func benchmarkRankingDB(b *testing.B, years int, index string) *sql.DB {
	b.Helper()
	db := benchmarkDB(b)
	if _, err := db.Exec(`CREATE TABLE agg_dim(day TEXT NOT NULL,dim TEXT NOT NULL,key TEXT NOT NULL,value INTEGER NOT NULL,PRIMARY KEY(day,dim,key)) WITHOUT ROWID`); err != nil {
		b.Fatal(err)
	}
	if index != "" {
		if _, err := db.Exec(`CREATE INDEX candidate ON agg_dim(` + index + `)`); err != nil {
			b.Fatal(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT INTO agg_dim VALUES(?,?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(years, 0, 0)
	begin := time.Now()
	count := 0
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		for _, dim := range []string{"origin", "sender", "relay", "client"} {
			for k := 0; k < 64; k++ {
				if _, err := stmt.Exec(day, dim, fmt.Sprintf("key%03d.example.org", k), 1); err != nil {
					b.Fatal(err)
				}
				count++
			}
		}
	}
	if err := stmt.Close(); err != nil {
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.Logf("seed: %d rows, %.0f ns/row", count, float64(time.Since(begin).Nanoseconds())/float64(count))
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		b.Fatal(err)
	}
	var pages, size int64
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		b.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		b.Fatal(err)
	}
	b.Logf("database: %d bytes", pages*size)
	return db
}

func BenchmarkRankingIndices(b *testing.B) {
	for _, years := range []int{3, 5} {
		for _, index := range []struct{ name, cols string }{{"primary", ""}, {"dim_day_key", "dim,day,key"}, {"dim_key_day", "dim,key,day"}} {
			for _, span := range []struct {
				name, first string
				days        int64
			}{{"year", "2022-01-01", 365}, {"month", "2022-12-01", 31}} {
				b.Run(fmt.Sprintf("years%d/%s/%s", years, index.name, span.name), func(b *testing.B) {
					db := benchmarkRankingDB(b, years, index.cols)
					q := &Q{DB: db, Loc: time.UTC}
					plan, err := db.Query(`EXPLAIN QUERY PLAN SELECT key,SUM(value) AS v FROM agg_dim WHERE day>=? AND day<=? AND dim=? GROUP BY key ORDER BY v DESC,key`, span.first, "2022-12-31", "sender")
					if err != nil {
						b.Fatal(err)
					}
					for plan.Next() {
						var a, c, d int
						var detail string
						if err := plan.Scan(&a, &c, &d, &detail); err != nil {
							b.Fatal(err)
						}
						b.Log(detail)
					}
					if err := plan.Err(); err != nil {
						b.Fatal(err)
					}
					if err := plan.Close(); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					for b.Loop() {
						rows, total, distinct, err := q.Ranking(span.first, "2022-12-31", "sender", 20)
						if err != nil {
							b.Fatal(err)
						}
						if total != span.days*64 || distinct != 64 || len(rows) != 20 || rows[0].Key != "key000.example.org" || rows[0].Value != span.days {
							b.Fatalf("changed ranking: %v %d %d", rows, total, distinct)
						}
					}
				})
			}
		}
	}
}

func BenchmarkRankingWriteBatch(b *testing.B) {
	for _, index := range []struct{ name, cols string }{{"primary", ""}, {"dim_day_key", "dim,day,key"}, {"dim_key_day", "dim,key,day"}} {
		b.Run(index.name, func(b *testing.B) {
			db := benchmarkRankingDB(b, 3, index.cols)
			b.ReportAllocs()
			// Recompute one whole day: the application's aggregate replacement model.
			for b.Loop() {
				tx, err := db.Begin()
				if err != nil {
					b.Fatal(err)
				}
				if _, err := tx.Exec(`DELETE FROM agg_dim WHERE day='2022-12-31'`); err != nil {
					_ = tx.Rollback()
					b.Fatal(err)
				}
				stmt, err := tx.Prepare(`INSERT INTO agg_dim VALUES('2022-12-31',?,?,1)`)
				if err != nil {
					_ = tx.Rollback()
					b.Fatal(err)
				}
				for _, dim := range []string{"origin", "sender", "relay", "client"} {
					for k := 0; k < 64; k++ {
						if _, err := stmt.Exec(dim, fmt.Sprintf("key%03d.example.org", k)); err != nil {
							_ = stmt.Close()
							_ = tx.Rollback()
							b.Fatal(err)
						}
					}
				}
				if err := stmt.Close(); err != nil {
					_ = tx.Rollback()
					b.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkExactDelays(b *testing.B) {
	for _, n := range []int{100000, 1000000} {
		b.Run(fmt.Sprintf("samples%d", n), func(b *testing.B) {
			db := benchmarkDB(b)
			if _, err := db.Exec(`CREATE TABLE deliveries(id INTEGER PRIMARY KEY,ts INTEGER NOT NULL,agent TEXT NOT NULL,delay REAL,class TEXT NOT NULL,status TEXT NOT NULL); CREATE INDEX deliveries_ts ON deliveries(ts,class,status)`); err != nil {
				b.Fatal(err)
			}
			if _, err := db.Exec(`WITH RECURSIVE seq(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM seq WHERE x<?) INSERT INTO deliveries(ts,agent,delay,class,status) SELECT 1,'smtp',x%10000,'delivery','sent' FROM seq`, n); err != nil {
				b.Fatal(err)
			}
			q := &Q{DB: db, Loc: time.UTC}
			b.ReportAllocs()
			for b.Loop() {
				stats, err := q.Delays(0, 2, "delivery")
				if err != nil {
					b.Fatal(err)
				}
				if stats.Count != n || stats.P50 != 4999 || stats.Max != 9999 {
					b.Fatalf("wrong exact stats: %+v", stats)
				}
			}
		})
	}
}

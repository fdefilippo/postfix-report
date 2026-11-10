// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package query

import (
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestAggregatedHourCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, zone, week string
		full             bool
		observed         int64
	}{
		{"one hour", "UTC", "2026-W42", false, 3600}, {"half hour", "UTC", "2026-W42", false, 1800}, {"empty", "UTC", "2026-W42", false, 0},
		{"complete", "UTC", "2026-W42", true, 0}, {"spring DST", "Europe/Rome", "2026-W13", true, 0}, {"fall DST", "Europe/Rome", "2026-W43", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			p, err := period.Parse(period.Week, tc.week, loc)
			if err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func(cleanup func() error) { _ = cleanup() }(db.Close)
			if tc.full || tc.observed > 0 {
				r, err := db.Exec(`INSERT INTO sources(fingerprint,fp_len,path,created_at,updated_at) VALUES('x',1,'x',0,0)`)
				if err != nil {
					t.Fatal(err)
				}
				id, _ := r.LastInsertId()
				lo, hi := p.Start.Unix(), p.End.Unix()-1
				if !tc.full {
					lo += 10 * 3600
					hi = lo + tc.observed - 1
				}
				if _, err := db.Exec(`INSERT INTO segments(source_id,host,start_ts,end_ts) VALUES(?,'mx',?,?)`, id, lo, hi); err != nil {
					t.Fatal(err)
				}
			}
			q := Q{DB: db, Loc: loc, Gap: 60}
			b, err := q.AggregatedHourBuckets(p)
			if err != nil {
				t.Fatal(err)
			}
			if len(b) != 24 {
				t.Fatalf("buckets=%d", len(b))
			}
			for h, v := range b {
				if tc.full {
					if v.Status != Complete || v.Fraction != 1 {
						t.Fatalf("hour %d: %+v", h, v)
					}
				} else if h == 10 && tc.observed > 0 {
					want := float64(tc.observed) / (7 * 3600)
					if v.Status != Partial || v.Covered != tc.observed || math.Abs(v.Fraction-want) > 1e-12 {
						t.Fatalf("hour 10: %+v want %v", v, want)
					}
				} else if v.Status != Missing || v.Fraction != 0 || v.Covered != 0 {
					t.Fatalf("hour %d: %+v", h, v)
				}
			}
			if tc.full && tc.zone == "Europe/Rome" {
				want := int64(6 * 3600)
				if tc.week == "2026-W43" {
					want = 8 * 3600
				}
				if b[2].Covered != want {
					t.Fatalf("DST hour: %+v, want %d seconds", b[2], want)
				}
			}
		})
	}
}

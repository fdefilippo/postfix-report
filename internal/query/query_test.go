// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package query

import "testing"

func TestDelayStatsNearestRank(t *testing.T) {
	v := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	d := delayStats(v, 55)
	if d.Count != 10 || d.Avg != 5.5 || d.P50 != 5 || d.P90 != 9 || d.P95 != 10 || d.P99 != 10 || d.Max != 10 {
		t.Errorf("%+v", d)
	}
	if d := delayStats(nil, 0); d.Count != 0 {
		t.Errorf("empty: %+v", d)
	}
	if d := delayStats([]float64{3}, 3); d.P50 != 3 || d.P99 != 3 {
		t.Errorf("single: %+v", d)
	}
}

func TestCovered(t *testing.T) {
	ivs := []Interval{{100, 199}, {300, 399}}
	if c := covered(ivs, 0, 1000); c != 200 {
		t.Errorf("covered = %d", c)
	}
	if c := covered(ivs, 150, 350); c != 50+50 {
		t.Errorf("partial = %d", c)
	}
	if st, f := status(0, 3600); st != Missing || f != 0 {
		t.Error("missing")
	}
	if st, _ := status(3600, 3600); st != Complete {
		t.Error("complete")
	}
	if st, f := status(1800, 3600); st != Partial || f != 0.5 {
		t.Error("partial")
	}
}

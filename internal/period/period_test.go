// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package period

import (
	"testing"
	"time"
)

func TestISOWeeks(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Rome")
	cases := map[string]string{
		"2026-10-11": "2026-W41", // Sunday
		"2026-10-12": "2026-W42", // Monday
		"2027-01-01": "2026-W53", // Friday: belongs to the last ISO week of 2026
		"2024-12-30": "2025-W01",
		"2021-01-03": "2020-W53",
	}
	for day, want := range cases {
		d, err := Parse(Day, day, loc)
		if err != nil {
			t.Fatal(err)
		}
		w := Of(Week, d.Start, loc)
		if w.Key != want {
			t.Errorf("%s: week %s, want %s", day, w.Key, want)
		}
		p, err := Parse(Week, want, loc)
		if err != nil || p.Start != w.Start {
			t.Errorf("Parse(%s) = %v %v", want, p.Start, err)
		}
		if len(p.Days()) != 7 || p.Start.Weekday() != time.Monday {
			t.Errorf("%s: days %v", want, p.Days())
		}
	}
	if _, err := Parse(Week, "2026-W53", loc); err != nil {
		t.Errorf("2026 has 53 ISO weeks: %v", err)
	}
	if _, err := Parse(Week, "2025-W53", loc); err == nil {
		t.Error("2025 has 52 ISO weeks")
	}
}

func TestDSTDayLength(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Rome")
	d, _ := Parse(Day, "2026-10-25", loc)
	if h := d.End.Sub(d.Start).Hours(); h != 25 {
		t.Errorf("fall-back day has %v hours", h)
	}
	d, _ = Parse(Day, "2026-03-29", loc)
	if h := d.End.Sub(d.Start).Hours(); h != 23 {
		t.Errorf("spring-forward day has %v hours", h)
	}
	if d.Next().Key != "2026-03-30" || d.Prev().Key != "2026-03-28" {
		t.Errorf("next/prev: %s %s", d.Next().Key, d.Prev().Key)
	}
}

func TestNavigationAndContaining(t *testing.T) {
	loc := time.UTC
	m, _ := Parse(Month, "2026-12", loc)
	if m.Next().Key != "2027-01" || m.Prev().Key != "2026-11" || len(m.Days()) != 31 {
		t.Errorf("month nav %s %s %d", m.Next().Key, m.Prev().Key, len(m.Days()))
	}
	y, _ := Parse(Year, "2026", loc)
	if y.Next().Key != "2027" || y.Path() != "yearly/2026.html" {
		t.Error("year")
	}
	c := Containing("2026-10-13", loc)
	if c[Day] != "2026-10-13" || c[Week] != "2026-W42" || c[Month] != "2026-10" || c[Year] != "2026" {
		t.Errorf("containing %v", c)
	}
}

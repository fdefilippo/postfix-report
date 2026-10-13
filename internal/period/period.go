// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package period implements the calendar periods used by the reports:
// local days, ISO 8601 weeks, months and years.
package period

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Kind is a period granularity.
type Kind string

const (
	Day   Kind = "day"
	Week  Kind = "week"
	Month Kind = "month"
	Year  Kind = "year"
)

// Kinds lists the granularities from finest to coarsest.
var Kinds = []Kind{Day, Week, Month, Year}

// Period is a half-open local-time interval [Start, End).
type Period struct {
	Kind  Kind
	Key   string // 2026-10-12, 2026-W42, 2026-10, 2026
	Start time.Time
	End   time.Time
}

// DayKey formats the local day of t.
func DayKey(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01-02") }

// DayKeyUnix formats the local day of a Unix timestamp.
func DayKeyUnix(ts int64, loc *time.Location) string { return DayKey(time.Unix(ts, 0), loc) }

// HourUnix returns the local hour (0..23) of a Unix timestamp.
func HourUnix(ts int64, loc *time.Location) int { return time.Unix(ts, 0).In(loc).Hour() }

func midnight(y int, m time.Month, d int, loc *time.Location) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// Of returns the period of kind k containing t.
func Of(k Kind, t time.Time, loc *time.Location) Period {
	t = t.In(loc)
	switch k {
	case Day:
		s := midnight(t.Year(), t.Month(), t.Day(), loc)
		return Period{Day, s.Format("2006-01-02"), s, midnight(t.Year(), t.Month(), t.Day()+1, loc)}
	case Week:
		wd := (int(t.Weekday()) + 6) % 7 // Monday = 0
		s := midnight(t.Year(), t.Month(), t.Day()-wd, loc)
		y, w := s.ISOWeek()
		return Period{Week, fmt.Sprintf("%04d-W%02d", y, w), s, midnight(s.Year(), s.Month(), s.Day()+7, loc)}
	case Month:
		s := midnight(t.Year(), t.Month(), 1, loc)
		return Period{Month, s.Format("2006-01"), s, midnight(t.Year(), t.Month()+1, 1, loc)}
	default:
		s := midnight(t.Year(), 1, 1, loc)
		return Period{Year, s.Format("2006"), s, midnight(t.Year()+1, 1, 1, loc)}
	}
}

// Parse parses a period key of the given kind.
func Parse(k Kind, key string, loc *time.Location) (Period, error) {
	bad := fmt.Errorf("invalid %s %q", k, key)
	switch k {
	case Day:
		t, err := time.ParseInLocation("2006-01-02", key, loc)
		if err != nil {
			return Period{}, bad
		}
		return Of(Day, t, loc), nil
	case Week:
		ys, ws, ok := strings.Cut(key, "-W")
		if !ok {
			return Period{}, bad
		}
		y, err1 := strconv.Atoi(ys)
		w, err2 := strconv.Atoi(ws)
		if err1 != nil || err2 != nil || w < 1 || w > 53 {
			return Period{}, bad
		}
		// 4 January is always in ISO week 1.
		jan4 := midnight(y, 1, 4, loc)
		p := Of(Week, jan4, loc)
		s := midnight(p.Start.Year(), p.Start.Month(), p.Start.Day()+7*(w-1), loc)
		res := Of(Week, s, loc)
		if res.Key != key {
			return Period{}, bad
		}
		return res, nil
	case Month:
		t, err := time.ParseInLocation("2006-01", key, loc)
		if err != nil {
			return Period{}, bad
		}
		return Of(Month, t, loc), nil
	case Year:
		y, err := strconv.Atoi(key)
		if err != nil || y < 1970 || y > 9999 {
			return Period{}, bad
		}
		return Of(Year, midnight(y, 1, 1, loc), loc), nil
	}
	return Period{}, bad
}

// Next returns the following period of the same kind.
func (p Period) Next() Period { return Of(p.Kind, p.End, p.Start.Location()) }

// Prev returns the preceding period of the same kind.
func (p Period) Prev() Period { return Of(p.Kind, p.Start.Add(-time.Hour), p.Start.Location()) }

// Days returns the local day keys in the period.
func (p Period) Days() []string {
	var out []string
	loc := p.Start.Location()
	for d := p.Start; d.Before(p.End); d = midnight(d.Year(), d.Month(), d.Day()+1, loc) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

// Contains reports whether the Unix timestamp ts lies in the period.
func (p Period) Contains(ts int64) bool {
	return ts >= p.Start.Unix() && ts < p.End.Unix()
}

// Containing returns, for a day key, the keys of the periods that contain it.
func Containing(day string, loc *time.Location) map[Kind]string {
	t, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return nil
	}
	out := map[Kind]string{}
	for _, k := range Kinds {
		out[k] = Of(k, t, loc).Key
	}
	return out
}

// Path returns the report file path (relative to the output root) for p.
func (p Period) Path() string {
	switch p.Kind {
	case Day:
		return "daily/" + p.Key + ".html"
	case Week:
		return "weekly/" + p.Key + ".html"
	case Month:
		return "monthly/" + p.Key + ".html"
	default:
		return "yearly/" + p.Key + ".html"
	}
}

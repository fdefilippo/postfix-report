// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import (
	"fmt"
	"time"
)

var months = map[string]time.Month{
	"Jan": time.January, "Feb": time.February, "Mar": time.March, "Apr": time.April,
	"May": time.May, "Jun": time.June, "Jul": time.July, "Aug": time.August,
	"Sep": time.September, "Oct": time.October, "Nov": time.November, "Dec": time.December,
}

// dstTolerance is how far back a timestamp may go, relative to the latest one
// already resolved, before an ambiguous wall-clock time is interpreted with the
// later of its two possible offsets.
const dstTolerance = 15 * time.Minute

// Clock resolves syslog timestamps to absolute instants.
//
// BSD syslog timestamps ("Oct 11 03:22:02") carry neither year nor offset.
// The year is inferred from a reference time (usually the file mtime) and
// advanced when the month wraps from December to January. Wall-clock times
// that occur twice when daylight saving time ends are disambiguated by
// keeping the resolved sequence monotonic.
type Clock struct {
	loc       *time.Location
	anchored  bool
	ref       time.Time
	year      int
	lastMonth time.Month
	last      time.Time
}

// ClockState is the persistent part of a Clock, saved with checkpoints so
// that a resumed import keeps the same year.
type ClockState struct {
	Year      int   `json:"year"`
	LastMonth int   `json:"last_month"`
	LastUnix  int64 `json:"last_unix"`
}

// NewClock creates a Clock. If fixedYear > 0 every BSD timestamp is placed
// in that year (with the usual December→January rollover). Otherwise the
// initial year is derived from reference when the first line is seen.
func NewClock(loc *time.Location, fixedYear int, reference time.Time, st *ClockState) *Clock {
	c := &Clock{loc: loc}
	if st != nil && st.Year > 0 {
		c.anchored = true
		c.year = st.Year
		c.lastMonth = time.Month(st.LastMonth)
		if st.LastUnix > 0 {
			c.last = time.Unix(st.LastUnix, 0)
		}
		return c
	}
	if fixedYear > 0 {
		c.anchored = true
		c.year = fixedYear
		return c
	}
	if reference.IsZero() {
		reference = time.Now()
	}
	c.ref = reference.In(loc)
	return c
}

// State returns the persistent state.
func (c *Clock) State() ClockState {
	st := ClockState{Year: c.year, LastMonth: int(c.lastMonth)}
	if !c.last.IsZero() {
		st.LastUnix = c.last.Unix()
	}
	return st
}

// anchor fixes the year for the first line of an unanchored clock: a line
// that would fall more than one day after the reference time (the file
// mtime, which cannot precede its own lines by much) belongs to the
// previous year.
func (c *Clock) anchor(mon time.Month, day int) {
	c.year = c.ref.Year()
	if time.Date(c.year, mon, day, 0, 0, 0, 0, c.loc).After(c.ref.Add(24 * time.Hour)) {
		c.year--
	}
	c.anchored = true
}

// ResolveBSD converts BSD syslog fields into an absolute time.
func (c *Clock) ResolveBSD(mon time.Month, day, hh, mm, ss int) (time.Time, error) {
	if !c.anchored {
		c.anchor(mon, day)
	}
	if c.lastMonth != 0 && mon < c.lastMonth && c.lastMonth-mon >= 6 {
		// e.g. Dec -> Jan: the log crossed into a new year.
		c.year++
	}
	c.lastMonth = mon
	if day < 1 || day > 31 || hh > 23 || mm > 59 || ss > 60 {
		return time.Time{}, fmt.Errorf("invalid timestamp %s %d %02d:%02d:%02d", mon, day, hh, mm, ss)
	}
	t := c.pick(c.year, mon, day, hh, mm, ss)
	if t.After(c.last) {
		c.last = t
	}
	return t, nil
}

// Observe records an absolute instant resolved elsewhere (RFC 3339).
func (c *Clock) Observe(t time.Time) {
	if t.After(c.last) {
		c.last = t
	}
	lt := t.In(c.loc)
	c.anchored = true
	c.year = lt.Year()
	c.lastMonth = lt.Month()
}

// pick returns the instant for a wall-clock time, choosing between the two
// candidates of an ambiguous (DST fall-back) time so that the sequence stays
// monotonic.
func (c *Clock) pick(y int, mon time.Month, day, hh, mm, ss int) time.Time {
	base := time.Date(y, mon, day, hh, mm, ss, 0, c.loc)
	var cands []time.Time
	for _, d := range []time.Duration{-time.Hour, 0, time.Hour} {
		t := base.Add(d)
		lt := t.In(c.loc)
		if lt.Year() == y && lt.Month() == mon && lt.Day() == day && lt.Hour() == hh && lt.Minute() == mm && lt.Second() == ss {
			cands = append(cands, t)
		}
	}
	if len(cands) == 0 {
		// Non-existent local time (spring forward gap): use Go's normalisation.
		return base
	}
	if len(cands) == 1 || c.last.IsZero() {
		return cands[0]
	}
	for _, t := range cands {
		if !t.Before(c.last.Add(-dstTolerance)) {
			return t
		}
	}
	return cands[len(cands)-1]
}

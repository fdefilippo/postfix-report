// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Command synthlog writes a synthetic Postfix maillog (fictitious data) for
// demos, benchmarks and end-to-end tests.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fdefilippo/postfix-report/internal/testutil"
)

func main() {
	start := flag.String("start", "2025-12-01", "first day (YYYY-MM-DD)")
	days := flag.Int("days", 75, "number of days")
	perDay := flag.Int("per-day", 400, "average accepted messages per weekday")
	missing := flag.String("missing", "40,41", "comma-separated day indexes without log lines")
	gap := flag.Int("gap-day", 20, "day index with a 3-hour hole (-1 = none)")
	seed := flag.Uint64("seed", 1, "random seed")
	tz := flag.String("timezone", "Europe/Rome", "timezone of the timestamps")
	flag.Parse()
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	s, err := time.ParseInLocation("2006-01-02", *start, loc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var miss []int
	for _, f := range strings.Split(*missing, ",") {
		if f = strings.TrimSpace(f); f != "" {
			n, err := strconv.Atoi(f)
			if err != nil {
				fmt.Fprintln(os.Stderr, "bad -missing:", err)
				os.Exit(2)
			}
			miss = append(miss, n)
		}
	}
	cfg := testutil.SynthConfig{Start: s, Days: *days, PerDay: *perDay, Missing: miss, GapDay: *gap, Seed: *seed}
	if err := testutil.Synthetic(os.Stdout, cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

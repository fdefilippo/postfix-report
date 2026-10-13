// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package app_test

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	ag "github.com/fdefilippo/postfix-report/internal/aggregate"
	"github.com/fdefilippo/postfix-report/internal/testutil"
	"github.com/fdefilippo/postfix-report/internal/verify"
)

// realExpected holds aggregate counts of the reference maillog, verified
// against an independent recount (scripts/independent_counts.py, see
// VALIDATION.md). Only numbers are stored here: no data from the log.
var realExpected = map[string]map[string]float64{
	"2026-10-11": {ag.MAccepted: 874, ag.MSent: 1068, ag.MDeferred: 936, ag.MBounced: 2, ag.MExpiredRcpt: 6, ag.MRejects: 144, ag.MNotifications: 6},
	"2026-10-12": {ag.MAccepted: 8212, ag.MSent: 8580, ag.MDeferred: 1117, ag.MBounced: 1551, ag.MExpiredRcpt: 11, ag.MRejects: 649, ag.MNotifications: 1562},
	"2026-10-13": {ag.MAccepted: 3806, ag.MSent: 4027, ag.MDeferred: 605, ag.MBounced: 321, ag.MExpiredRcpt: 12, ag.MRejects: 281, ag.MNotifications: 327},
}

var realTotal = map[string]float64{
	ag.MAccepted: 12892, ag.MAcceptedSMTP: 12889, ag.MAcceptedLocal: 3, ag.MRecipients: 13690, ag.MBytes: 3276848804,
	ag.MSent: 13675, ag.MDeferred: 2658, ag.MBounced: 1874, ag.MExpiredRcpt: 29, ag.MExpiredMsg: 29, ag.MFinalFailures: 1903,
	ag.MRejects: 1074, ag.MRejectsTemp: 946, ag.MRejectsPerm: 128, ag.MNotifications: 1895, ag.MFilterHandoff: 13687,
	ag.MConnections: 20428, ag.MSessions: 20426, ag.MSessionsTLS: 12732, ag.MSASLOK: 391, ag.MSASLFail: 1691, ag.MDelayCount: 13675,
}

func realLines(t *testing.T) []string {
	path := testutil.RealLogPath()
	if path == "" {
		t.Skip("reference maillog not available")
	}
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func checkReal(t *testing.T, env *testutil.Env) {
	a := env.Open(t)
	defer a.Close()
	for day, want := range realExpected {
		expect(t, totals(t, a, day), want)
	}
	expect(t, totals(t, a, "2026-10-11", "2026-10-12", "2026-10-13"), realTotal)
	r, err := verify.Run(a.DB, a.Cfg.Location, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Checks {
		if !c.OK && !c.Warning {
			t.Errorf("verify %s: %s", c.Name, c.Detail)
		}
	}
}

// TestRealLogRegression imports the reference log in one pass.
func TestRealLogRegression(t *testing.T) {
	lines := realLines(t)
	env := testutil.NewEnv(t)
	env.Cfg.Input.Year = 2026
	testutil.WriteFile(t, env.LogPath("maillog"), strings.Join(lines, "\n")+"\n", time.Time{})
	a := env.Open(t)
	if _, err := a.Import(ctx, "test", []string{env.LogPath("maillog")}); err != nil {
		t.Fatal(err)
	}
	a.Close()
	checkReal(t, env)
}

// TestRealLogIncrementalWithRotation feeds the same log as a growing file
// read in five runs, rotated and compressed halfway, and requires the exact
// same statistics as the single-pass import.
func TestRealLogIncrementalWithRotation(t *testing.T) {
	lines := realLines(t)
	env := testutil.NewEnv(t)
	env.Cfg.Input.Year = 2026
	cur := env.LogPath("maillog")
	n := len(lines)
	cuts := []int{n / 5, 2 * n / 5, 3 * n / 5, 4 * n / 5, n}
	rotateAt := 2 // after the third run the file is rotated
	start := 0    // first line of the current file
	for i, c := range cuts {
		testutil.WriteFile(t, cur, strings.Join(lines[start:c], "\n")+"\n", time.Time{})
		paths := []string{cur}
		if i > rotateAt {
			paths = append([]string{env.LogPath("maillog-1.gz")}, paths...)
		}
		a := env.Open(t)
		if _, err := a.Import(ctx, "update", paths); err != nil {
			t.Fatal(err)
		}
		a.Close()
		if i == rotateAt {
			// logrotate: compress the current file, start a new one.
			testutil.WriteFile(t, env.LogPath("maillog-1.gz"), strings.Join(lines[start:c], "\n")+"\n", time.Time{})
			start = c
		}
	}
	checkReal(t, env)
}

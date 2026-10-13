// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

// RealLogPath returns the path of the reference maillog used by regression
// tests. It is never distributed: tests skip when it is absent.
func RealLogPath() string {
	if p := os.Getenv("POSTFIX_REPORT_REAL_LOG"); p != "" {
		return p
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	for _, p := range []string{filepath.Join(root, "samples", "maillog"), filepath.Join(root, "maillog")} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// TestRealLogClassification checks that every line of the reference log is
// recognized (no KindUnknown, no malformed header) and that the per-kind
// counts match independent counts done with rg/awk (see VALIDATION.md).
func TestRealLogClassification(t *testing.T) {
	path := RealLogPath()
	if path == "" {
		t.Skip("reference maillog not available")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	loc, _ := time.LoadLocation("Europe/Rome")
	clock := NewClock(loc, 2026, time.Time{}, nil)
	counts := map[Kind]int{}
	unknown := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	lines := 0
	for sc.Scan() {
		lines++
		ev, err := Parse(sc.Text(), clock)
		if err != nil {
			t.Fatalf("line %d: %v: %s", lines, err, sc.Text())
		}
		counts[ev.Kind]++
		if ev.Kind == KindUnknown {
			unknown[ev.Proc+": "+trunc(ev.Raw, 120)]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("%-14s %7d", k, counts[Kind(k)])
	}
	for u, n := range unknown {
		t.Errorf("unknown (%d): %s", n, u)
	}
	expect := map[Kind]int{
		KindConnect:      20428,
		KindDisconnect:   20426,
		KindActive:       30321,
		KindRemoved:      27692,
		KindExpired:      29,
		KindPickup:       12892,
		KindNotification: 1895,
		KindSASLFail:     1691,
		KindDelivery:     27362 + 2658 + 1874 + 23,
		KindMessageID:    28727,
	}
	for k, want := range expect {
		if counts[k] != want {
			t.Errorf("%s: got %d, want %d", k, counts[k], want)
		}
	}
	if lines != 254884 {
		t.Errorf("lines = %d", lines)
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

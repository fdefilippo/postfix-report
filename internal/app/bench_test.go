// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package app_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/testutil"
)

// BenchmarkImportPipeline measures import + correlation + aggregation of a
// synthetic week (about 30,000 lines); lines/s is reported.
func BenchmarkImportPipeline(b *testing.B) {
	loc, _ := time.LoadLocation("Europe/Rome")
	var buf bytes.Buffer
	if err := testutil.Synthetic(&buf, testutil.SynthConfig{Start: time.Date(2026, 3, 2, 0, 0, 0, 0, loc), Days: 7, PerDay: 400, GapDay: -1, Seed: 1}); err != nil {
		b.Fatal(err)
	}
	content := buf.String()
	lines := strings.Count(content, "\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		env := testutil.NewEnv(b)
		testutil.WriteFile(b, env.LogPath("maillog"), content, time.Date(2026, 3, 10, 0, 0, 0, 0, loc))
		a := env.Open(b)
		b.StartTimer()
		if _, err := a.Import(ctx, "bench", []string{env.LogPath("maillog")}); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		a.Close()
		b.StartTimer()
	}
	b.ReportMetric(float64(lines)*float64(b.N)/b.Elapsed().Seconds(), "lines/s")
}

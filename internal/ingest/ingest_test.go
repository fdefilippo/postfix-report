// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package ingest_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/ingest"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

func line(i int) string {
	return "Oct 12 10:00:" + twoDigits(i%60) + " mx1 postfix/smtpd[1]: connect from unknown[192.0.2." + itoa(i%250) + "]"
}

func twoDigits(i int) string {
	s := itoa(i)
	if len(s) < 2 {
		s = "0" + s
	}
	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func importer(t *testing.T, env *testutil.Env) *ingest.Importer {
	a := env.Open(t)
	t.Cleanup(func() { _ = a.Close() })
	// import_files rows reference an import run.
	for i := 0; i < 3; i++ {
		if _, err := a.DB.Exec(`INSERT INTO imports(command, started_at, status) VALUES('test', 0, 'ok')`); err != nil {
			t.Fatal(err)
		}
	}
	return &ingest.Importer{DB: a.DB, Cfg: env.Cfg, Log: env.Log}
}

func TestSmallFileGrowsPastFingerprint(t *testing.T) {
	env := testutil.NewEnv(t)
	im := importer(t, env)
	p := env.LogPath("maillog")
	mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, line(i))
	}
	testutil.WriteFile(t, p, strings.Join(lines[:2], "\n")+"\n", mt) // < 4096 bytes
	r1, err := im.Import(context.Background(), 1, []string{p})
	if err != nil || r1.Events != 2 {
		t.Fatalf("first: %v events, %v", r1.Events, err)
	}
	testutil.WriteFile(t, p, strings.Join(lines, "\n")+"\n", mt) // > 4096 bytes
	r2, err := im.Import(context.Background(), 2, []string{p})
	if err != nil || r2.Events != 198 || r2.Duplicates != 0 {
		t.Fatalf("second: %v events, %v duplicates, %v", r2.Events, r2.Duplicates, err)
	}
	if r2.Files[0].SourceID != r1.Files[0].SourceID {
		t.Error("grown file seen as a new source")
	}
}

func TestCopyTruncateStartsNewSource(t *testing.T) {
	env := testutil.NewEnv(t)
	im := importer(t, env)
	p := env.LogPath("maillog")
	mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	testutil.WriteFile(t, p, line(1)+"\n"+line(2)+"\n"+line(3)+"\n", mt)
	if _, err := im.Import(context.Background(), 1, []string{p}); err != nil {
		t.Fatal(err)
	}
	// copytruncate: same path, new shorter content.
	testutil.WriteFile(t, p, line(7)+"\n", mt)
	r, err := im.Import(context.Background(), 2, []string{p})
	if err != nil || r.Events != 1 {
		t.Fatalf("after truncate: %v events, %v", r.Events, err)
	}
}

func TestMissingFileIsReported(t *testing.T) {
	env := testutil.NewEnv(t)
	im := importer(t, env)
	_, err := im.Import(context.Background(), 1, []string{env.LogPath("nope")})
	if err == nil || !os.IsNotExist(unwrapAll(err)) {
		t.Errorf("err = %v", err)
	}
}

func unwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}

func TestExpandPaths(t *testing.T) {
	env := testutil.NewEnv(t)
	for _, n := range []string{"maillog", "maillog-1.gz", "other"} {
		testutil.WriteFile(t, env.LogPath(n), "x\n", time.Time{})
	}
	got := ingest.ExpandPaths([]string{env.LogPath("maillog*"), env.LogPath("absent")})
	if len(got) != 3 || !strings.HasSuffix(got[2], "absent") {
		t.Errorf("%v", got)
	}
}

func TestSharedHeadDivergenceAndCopies(t *testing.T) {
	for _, ext := range []string{".log", ".gz"} {
		t.Run(ext, func(t *testing.T) {
			env := testutil.NewEnv(t)
			im := importer(t, env)
			head := strings.Repeat("Oct 12 09:00:00 mx1 postfix/anvil[1]: statistics: max cache size 1 at x\n", 80)
			if len(head) < ingest.FingerprintLen {
				t.Fatal("short test head")
			}
			mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
			a, b := env.LogPath("a"+ext), env.LogPath("b"+ext)
			testutil.WriteFile(t, a, head+line(1)+"\n", mt)
			testutil.WriteFile(t, b, head+line(2)+"\n", mt)
			r1, err := im.Import(context.Background(), 1, []string{a})
			if err != nil || r1.Events != 1 {
				t.Fatalf("first: %+v %v", r1, err)
			}
			r2, err := im.Import(context.Background(), 2, []string{b})
			if err != nil || r2.Events != 1 || r2.Files[0].SourceID == r1.Files[0].SourceID {
				t.Fatalf("divergence: %+v %v", r2, err)
			}
			r3, err := im.Import(context.Background(), 3, []string{a, b})
			if err != nil || r3.Events != 0 || r3.Lines != 0 {
				t.Fatalf("repeat: %+v %v", r3, err)
			}
		})
	}
}

func TestRewrittenConsumedTailAndUnconsumedPartial(t *testing.T) {
	env := testutil.NewEnv(t)
	im := importer(t, env)
	p := env.LogPath("mail")
	head := strings.Repeat("Oct 12 09:00:00 mx1 postfix/anvil[1]: statistics: max cache size 1 at x\n", 80)
	mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	testutil.WriteFile(t, p, head+line(1)+"\npartial", mt)
	first, err := im.Import(context.Background(), 1, []string{p})
	if err != nil {
		t.Fatal(err)
	}
	// Completing/replacing the unconsumed partial line preserves the checkpoint.
	testutil.WriteFile(t, p, head+line(1)+"\n"+line(2)+"\n", mt)
	second, err := im.Import(context.Background(), 2, []string{p})
	if err != nil || second.Events != 1 || second.Files[0].SourceID != first.Files[0].SourceID {
		t.Fatalf("partial: %+v %v", second, err)
	}
	// An edit of the consumed suffix beyond 4 KiB creates a branch.
	testutil.WriteFile(t, p, head+line(1)+"\n"+line(3)+"\n", mt)
	third, err := im.Import(context.Background(), 3, []string{p})
	if err != nil || third.Events != 1 || third.Files[0].SourceID == first.Files[0].SourceID {
		t.Fatalf("rewrite: %+v %v", third, err)
	}
}

func TestGzipCopyThenAppend(t *testing.T) {
	for _, ext := range []string{".log", ".gz"} {
		t.Run(ext, func(t *testing.T) {
			env := testutil.NewEnv(t)
			im := importer(t, env)
			mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
			p := env.LogPath("a.gz")
			testutil.WriteFile(t, p, line(1)+"\n", mt)
			first, err := im.Import(context.Background(), 1, []string{p})
			if err != nil {
				t.Fatal(err)
			}
			p = env.LogPath("b" + ext)
			testutil.WriteFile(t, p, line(1)+"\n"+line(2)+"\n", mt)
			second, err := im.Import(context.Background(), 2, []string{p})
			if err != nil || second.Events != 1 || second.Files[0].SourceID != first.Files[0].SourceID {
				t.Fatalf("append after gzip: %+v %v", second, err)
			}
		})
	}
}

func TestLegacyLongCheckpointReplaysOnce(t *testing.T) {
	env := testutil.NewEnv(t)
	im := importer(t, env)
	p := env.LogPath("a.gz")
	mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	head := strings.Repeat("Oct 12 09:00:00 mx1 postfix/anvil[1]: statistics: max cache size 1 at x\n", 80)
	testutil.WriteFile(t, p, head+line(1)+"\n", mt)
	first, err := im.Import(context.Background(), 1, []string{p})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.DB.Exec(`UPDATE sources SET checkpoint_hash=''`); err != nil {
		t.Fatal(err)
	}
	second, err := im.Import(context.Background(), 2, []string{p})
	if err != nil || second.Events != 0 || second.Duplicates != 1 || second.Files[0].SourceID == first.Files[0].SourceID {
		t.Fatalf("legacy replay: %+v %v", second, err)
	}
	third, err := im.Import(context.Background(), 3, []string{p})
	if err != nil || third.Events != 0 || third.Lines != 0 {
		t.Fatalf("legacy repeat: %+v %v", third, err)
	}
}

func TestLogLineLimitAndRetry(t *testing.T) {
	for _, ext := range []string{".log", ".gz"} {
		for _, extra := range []int{0, 1} {
			for _, newline := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/extra%d/newline%t", ext, extra, newline), func(t *testing.T) {
					env := testutil.NewEnv(t)
					im := importer(t, env)
					p := env.LogPath("mail" + ext)
					mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
					suffix := ""
					if newline {
						suffix = "\n"
					}
					body := strings.Repeat("x", ingest.MaxLogLineBytes+extra-len(suffix)) + suffix
					testutil.WriteFile(t, p, line(1)+"\n"+body, mt)
					_, err := im.Import(context.Background(), 1, []string{p})
					if extra == 1 {
						if !errors.Is(err, ingest.ErrLineTooLong) {
							t.Fatalf("expected line limit: %v", err)
						}
						var off int64
						if err := im.DB.QueryRow(`SELECT offset FROM sources LIMIT 1`).Scan(&off); err != nil || off != 0 {
							t.Fatalf("uncommitted checkpoint %d %v", off, err)
						}
						// Retrying a failed source after replacing the bad suffix preserves
						// complete-event identity and never imports a truncated prefix.
						testutil.WriteFile(t, p, line(1)+"\n"+line(2)+"\n", mt)
						if _, err := im.Import(context.Background(), 2, []string{p}); err != nil {
							t.Fatal(err)
						}
						var n int
						if err := im.DB.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil || n != 2 {
							t.Fatalf("events=%d %v", n, err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

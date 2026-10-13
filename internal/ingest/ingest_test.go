// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package ingest_test

import (
	"context"
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
	t.Cleanup(func() { a.Close() })
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

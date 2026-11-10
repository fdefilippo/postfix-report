// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package ingest_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fdefilippo/postfix-report/internal/ingest"
	"github.com/fdefilippo/postfix-report/internal/testutil"
)

func TestOversizedLinePreservesCommittedCheckpoint(t *testing.T) {
	for _, ext := range []string{".log", ".gz"} {
		t.Run(ext, func(t *testing.T) {
			e := testutil.NewEnv(t)
			im := importer(t, e)
			p := e.LogPath("mail" + ext)
			mt := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
			// Exactly one full ingest batch, followed by an oversized line. Repeated
			// non-Postfix lines are counted as input but must not invent events.
			head := strings.Repeat("not a Postfix log line\n", 5000)
			testutil.WriteFile(t, p, head+strings.Repeat("x", ingest.MaxLogLineBytes+1)+"\n", mt)
			if _, err := im.Import(context.Background(), 1, []string{p}); !errors.Is(err, ingest.ErrLineTooLong) {
				t.Fatalf("limit: %v", err)
			}
			var offset, lines int64
			if err := im.DB.QueryRow(`SELECT offset,lines FROM sources`).Scan(&offset, &lines); err != nil || offset != int64(len(head)) || lines != 5000 {
				t.Fatalf("checkpoint=%d/%d %v", offset, lines, err)
			}
			testutil.WriteFile(t, p, head+line(1)+"\n", mt)
			r, err := im.Import(context.Background(), 2, []string{p})
			if err != nil || r.Events != 1 || r.Lines != 1 {
				t.Fatalf("retry=%+v %v", r, err)
			}
			if err := im.DB.QueryRow(`SELECT offset,lines FROM sources`).Scan(&offset, &lines); err != nil || lines != 5001 {
				t.Fatalf("retry checkpoint=%d/%d %v", offset, lines, err)
			}
		})
	}
}

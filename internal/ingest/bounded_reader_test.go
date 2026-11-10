// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package ingest

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type cancellingLineReader struct {
	io.Reader
	cancel context.CancelFunc
	reads  int
}

func (r *cancellingLineReader) Read(p []byte) (int, error) {
	r.reads++
	n, err := r.Reader.Read(p)
	r.cancel()
	return n, err
}

func TestReadLogLineCancelsBetweenFragments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &cancellingLineReader{Reader: strings.NewReader(strings.Repeat("x", MaxLogLineBytes)), cancel: cancel}
	if _, err := readLogLine(ctx, bufio.NewReaderSize(r, 32)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if r.reads != 1 {
		t.Fatalf("read %d chunks after cancellation", r.reads)
	}
}

// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointHashCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 8192)), 0600); err != nil {
		t.Fatal(err)
	}
	lf, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func(cleanup func() error) { _ = cleanup() }(lf.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lf.validateCheckpoint(ctx, 8192, "wrong"); !errors.Is(err, context.Canceled) {
		t.Fatalf("checkpoint cancellation: %v", err)
	}
}

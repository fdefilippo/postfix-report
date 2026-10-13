// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package ingest

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// FingerprintLen is the number of leading (decompressed) bytes that identify a source.
const FingerprintLen = 4096

// logFile is an opened log file, transparently decompressed.
type logFile struct {
	path       string
	f          *os.File
	r          io.Reader // decompressed stream positioned after head
	head       []byte    // first bytes of the decompressed stream
	compressed bool
	size       int64
	mtime      int64
	gz         *gzip.Reader
}

func openLog(path string) (*logFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	lf := &logFile{path: path, f: f, size: st.Size(), mtime: st.ModTime().Unix()}
	br := bufio.NewReaderSize(f, 1<<16)
	magic, _ := br.Peek(2)
	var r io.Reader = br
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		lf.gz = gz
		lf.compressed = true
		r = gz
	}
	head := make([]byte, FingerprintLen)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	lf.head = head[:n]
	lf.r = io.MultiReader(bytes.NewReader(lf.head), r)
	return lf, nil
}

func (lf *logFile) Close() error {
	if lf.gz != nil {
		lf.gz.Close()
	}
	return lf.f.Close()
}

// fingerprint returns the hash of the first n bytes of the head.
func (lf *logFile) fingerprint(n int) string {
	if n > len(lf.head) {
		n = len(lf.head)
	}
	s := sha256.Sum256(lf.head[:n])
	return hex.EncodeToString(s[:])
}

// skip discards n bytes of the decompressed stream (used to resume).
func (lf *logFile) skip(n int64) error {
	if n == 0 {
		return nil
	}
	if !lf.compressed {
		// Plain file: seek directly instead of reading.
		if _, err := lf.f.Seek(n, io.SeekStart); err != nil {
			return err
		}
		lf.r = bufio.NewReaderSize(lf.f, 1<<16)
		return nil
	}
	m, err := io.CopyN(io.Discard, lf.r, n)
	if err != nil {
		return fmt.Errorf("%s: cannot resume at offset %d (read %d): %w", lf.path, n, m, err)
	}
	return nil
}

// firstLine returns the first complete line of the head ("" if none).
func (lf *logFile) firstLine() string {
	if i := bytes.IndexByte(lf.head, '\n'); i >= 0 {
		return string(lf.head[:i])
	}
	return ""
}

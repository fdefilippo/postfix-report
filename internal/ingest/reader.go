// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package ingest

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
)

// FingerprintLen is the head size used to select source candidates.
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
	digest     hash.Hash
}

func openLog(path string) (*logFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	lf := &logFile{path: path, f: f, size: st.Size(), mtime: st.ModTime().Unix()}
	br := bufio.NewReaderSize(f, 1<<16)
	magic, _ := br.Peek(2)
	var r io.Reader = br
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		lf.gz = gz
		lf.compressed = true
		r = gz
	}
	head := make([]byte, FingerprintLen)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	lf.head = head[:n]
	lf.r = io.MultiReader(bytes.NewReader(lf.head), r)
	return lf, nil
}

func (lf *logFile) Close() error {
	if lf.gz != nil {
		_ = lf.gz.Close()
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

// firstLine returns the first complete line of the head ("" if none).
func (lf *logFile) firstLine() string {
	if i := bytes.IndexByte(lf.head, '\n'); i >= 0 {
		return string(lf.head[:i])
	}
	return ""
}

// reset reopens the decompression stream on the same descriptor, so candidate
// validation never switches to a different file after a rename.
func (lf *logFile) reset() error {
	if lf.gz != nil {
		if err := lf.gz.Close(); err != nil {
			return err
		}
	}
	if _, err := lf.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReaderSize(lf.f, 1<<16)
	if lf.compressed {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		lf.gz, lf.r = gz, gz
	} else {
		lf.r = br
	}
	lf.digest = sha256.New()
	return nil
}

// validateCheckpoint consumes and hashes exactly the checkpointed bytes.
// A mismatch or truncation is a different source, never a resumable prefix.
func (lf *logFile) validateCheckpoint(ctx context.Context, offset int64, digest string) (bool, error) {
	if err := lf.reset(); err != nil {
		return false, err
	}
	if _, err := io.CopyN(lf.digest, checkpointReader{ctx, lf.r}, offset); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, err
	}
	return hex.EncodeToString(lf.digest.Sum(nil)) == digest, nil
}

// Hashing a large checkpoint checks cancellation between bounded reads.
type checkpointReader struct {
	ctx context.Context
	r   io.Reader
}

func (r checkpointReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

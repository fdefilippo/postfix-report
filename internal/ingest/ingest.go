// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package ingest reads Postfix log files incrementally and stores normalized
// events. Import is idempotent: sources are identified by content
// fingerprint, progress is checkpointed in the same transaction as the events,
// and lines already stored (also from overlapping sources) are ignored.
package ingest

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/logparse"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
)

// batchLines is the number of lines committed per transaction.
const batchLines = 5000

// Importer imports log files into the database.
type Importer struct {
	DB  *store.DB
	Cfg *config.Config
	Log *slog.Logger
}

// FileResult summarizes the import of one file.
type FileResult struct {
	Path       string
	SourceID   int64
	Skipped    string // reason, if nothing was read
	Lines      int64
	Events     int64
	Duplicates int64
	NonPostfix int64
	Malformed  int64
	Unknown    int64
	StartOff   int64
	EndOff     int64
	FirstTS    int64
	LastTS     int64
}

// Result summarizes an import run.
type Result struct {
	Files      []FileResult
	Lines      int64
	Events     int64
	Duplicates int64
	// MinTS is, per host, the earliest timestamp of a newly stored event.
	MinTS map[string]int64
	// DirtyDays are the local days with new lines (events or coverage).
	DirtyDays map[string]bool
}

type sourceRow struct {
	id         int64
	offset     int64
	lines      int64
	firstTS    sql.NullInt64
	lastTS     sql.NullInt64
	clockState *logparse.ClockState
	tail       tailState
	complete   bool
	compressed bool
	fileSize   int64
	fileMtime  int64
}

// tailState holds the occurrence counters of identical lines within the last
// second read, so that a resumed import keys duplicates consistently.
type tailState struct {
	TS     int64          `json:"ts"`
	Counts map[string]int `json:"counts"`
	cnt    map[uint64]int // working copy
}

func (t *tailState) load() {
	t.cnt = map[uint64]int{}
	for k, v := range t.Counts {
		if h, err := strconv.ParseUint(k, 16, 64); err == nil {
			t.cnt[h] = v
		}
	}
}

func (t *tailState) dump() string {
	t.Counts = map[string]int{}
	for k, v := range t.cnt {
		t.Counts[strconv.FormatUint(k, 16)] = v
	}
	b, _ := json.Marshal(t)
	return string(b)
}

// ExpandPaths expands glob patterns; non-matching literal paths are kept so
// that a missing file is reported.
func ExpandPaths(patterns []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range patterns {
		m, _ := filepath.Glob(p)
		if len(m) == 0 && !hasMeta(p) {
			m = []string{p}
		}
		for _, x := range m {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	return out
}

func hasMeta(p string) bool {
	for _, c := range p {
		switch c {
		case '*', '?', '[':
			return true
		}
	}
	return false
}

// DirFiles lists the regular files of a directory (non recursive).
func DirFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

// Import imports the given files, oldest first.
func (im *Importer) Import(ctx context.Context, importID int64, paths []string) (*Result, error) {
	res := &Result{MinTS: map[string]int64{}, DirtyDays: map[string]bool{}}
	ordered := im.orderByFirstTimestamp(paths)
	var firstErr error
	for _, p := range ordered {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		fr, err := im.importFile(ctx, importID, p, res)
		if err != nil {
			im.Log.Error("import failed", "file", p, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", p, err)
			}
			continue
		}
		res.Files = append(res.Files, fr)
		res.Lines += fr.Lines
		res.Events += fr.Events
		res.Duplicates += fr.Duplicates
		if fr.Skipped != "" {
			im.Log.Debug("file skipped", "file", p, "reason", fr.Skipped)
		} else {
			im.Log.Info("file imported", "file", p, "lines", fr.Lines, "events", fr.Events,
				"duplicates", fr.Duplicates, "non_postfix", fr.NonPostfix, "unknown", fr.Unknown)
		}
	}
	return res, firstErr
}

// orderByFirstTimestamp sorts files by the timestamp of their first line so
// that rotated files are processed before the current one.
func (im *Importer) orderByFirstTimestamp(paths []string) []string {
	type item struct {
		path string
		ts   int64
	}
	var items []item
	for _, p := range paths {
		ts := int64(1<<62 - 1)
		if lf, err := openLog(p); err == nil {
			clock := logparse.NewClock(im.Cfg.Location, im.Cfg.Input.Year, time.Unix(lf.mtime, 0), nil)
			if h, err := logparse.ParseHeader(lf.firstLine(), clock); err == nil {
				ts = h.TS.Unix()
			}
			lf.Close()
		}
		items = append(items, item{p, ts})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ts < items[j].ts })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.path
	}
	return out
}

// findSource locates the stored source for lf, creating it if new.
func (im *Importer) findSource(lf *logFile) (*sourceRow, error) {
	if len(lf.head) == 0 {
		return nil, nil
	}
	n := len(lf.head)
	fp := lf.fingerprint(n)
	src, err := im.loadSource(`fingerprint=? AND fp_len=?`, fp, n)
	if err != nil || src != nil {
		return src, err
	}
	// A source first seen while shorter than FingerprintLen may have grown.
	rows, err := im.DB.Query(`SELECT id, fp_len, fingerprint FROM sources WHERE fp_len < ?`, n)
	if err != nil {
		return nil, err
	}
	var grownID int64
	for rows.Next() {
		var id int64
		var l int
		var f string
		if err := rows.Scan(&id, &l, &f); err != nil {
			rows.Close()
			return nil, err
		}
		if lf.fingerprint(l) == f {
			grownID = id
			break
		}
	}
	rows.Close()
	if grownID != 0 {
		if _, err := im.DB.Exec(`UPDATE sources SET fingerprint=?, fp_len=? WHERE id=?`, fp, n, grownID); err != nil {
			return nil, err
		}
		return im.loadSource(`id=?`, grownID)
	}
	now := time.Now().Unix()
	r, err := im.DB.Exec(`INSERT INTO sources(fingerprint, fp_len, path, compressed, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		fp, n, lf.path, lf.compressed, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := r.LastInsertId()
	return im.loadSource(`id=?`, id)
}

func (im *Importer) loadSource(where string, args ...any) (*sourceRow, error) {
	s := &sourceRow{}
	var clock, tail sql.NullString
	err := im.DB.QueryRow(`SELECT id, offset, lines, first_ts, last_ts, clock_state, tail_state, complete, compressed, file_size, file_mtime
		FROM sources WHERE `+where, args...).Scan(&s.id, &s.offset, &s.lines, &s.firstTS, &s.lastTS, &clock, &tail,
		&s.complete, &s.compressed, &s.fileSize, &s.fileMtime)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if clock.Valid && clock.String != "" {
		s.clockState = &logparse.ClockState{}
		if err := json.Unmarshal([]byte(clock.String), s.clockState); err != nil {
			s.clockState = nil
		}
	}
	if tail.Valid && tail.String != "" {
		_ = json.Unmarshal([]byte(tail.String), &s.tail)
	}
	s.tail.load()
	return s, nil
}

const insertEvent = `INSERT OR IGNORE INTO events(source_id, line_offset, ukey, ts, host, inst, proc, pid, qid, kind,
	sender, rcpt, orig, client, ip, relay, size, nrcpt, delay, delays, dsn, status, code, stage, text,
	method, user, uid, new_qid, tls, auth_ok, auth_ko)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// segState is an observed interval; id 0 means not yet stored.
type segState struct {
	id         int64
	host       string
	start, end int64
}

type unknownAgg struct {
	count  int64
	sample string
}

// fileImport is the state of one file being imported.
type fileImport struct {
	im       *Importer
	src      *sourceRow
	lf       *logFile
	fr       *FileResult
	res      *Result
	clock    *logparse.Clock
	tx       *sql.Tx
	segs     map[string]*segState
	closed   []segState // segments ended by a gap, to flush
	unknown  map[string]*unknownAgg
	gap      int64
	dayStart int64
	dayEnd   int64
	hosts    map[string]bool
}

func (im *Importer) importFile(ctx context.Context, importID int64, path string, res *Result) (FileResult, error) {
	fr := FileResult{Path: path}
	lf, err := openLog(path)
	if err != nil {
		return fr, err
	}
	defer lf.Close()
	src, err := im.findSource(lf)
	if err != nil {
		return fr, err
	}
	if src == nil {
		fr.Skipped = "empty file"
		return fr, nil
	}
	fr.SourceID = src.id
	fr.StartOff, fr.EndOff = src.offset, src.offset
	if _, err := im.DB.Exec(`UPDATE sources SET path=? WHERE id=?`, path, src.id); err != nil {
		return fr, err
	}
	switch {
	case src.complete:
		fr.Skipped = "already fully imported"
		return fr, nil
	case !lf.compressed && lf.size == src.offset:
		fr.Skipped = "no new data"
		return fr, nil
	case !lf.compressed && lf.size < src.offset:
		return fr, fmt.Errorf("file is shorter (%d) than the imported offset (%d): truncated or replaced", lf.size, src.offset)
	}
	if err := lf.skip(src.offset); err != nil {
		return fr, err
	}
	fi := &fileImport{
		im: im, src: src, lf: lf, fr: &fr, res: res,
		clock:   logparse.NewClock(im.Cfg.Location, im.Cfg.Input.Year, time.Unix(lf.mtime, 0), src.clockState),
		segs:    map[string]*segState{},
		unknown: map[string]*unknownAgg{},
		gap:     int64(im.Cfg.Coverage.GapThreshold.Seconds()),
	}
	if len(im.Cfg.Input.Hosts) > 0 {
		fi.hosts = map[string]bool{}
		for _, h := range im.Cfg.Input.Hosts {
			fi.hosts[h] = true
		}
	}
	err = fi.run(ctx)
	if err == nil {
		_, err = im.DB.Exec(`INSERT INTO import_files(import_id, source_id, path, start_offset, end_offset, lines, events,
			duplicates, non_postfix, malformed, unknown, first_ts, last_ts) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			importID, src.id, path, fr.StartOff, fr.EndOff, fr.Lines, fr.Events, fr.Duplicates, fr.NonPostfix,
			fr.Malformed, fr.Unknown, nullTS(fr.FirstTS), nullTS(fr.LastTS))
	}
	return fr, err
}

func nullTS(ts int64) any {
	if ts == 0 {
		return nil
	}
	return ts
}

func (fi *fileImport) run(ctx context.Context) error {
	br := bufio.NewReaderSize(fi.lf.r, 1<<20)
	offset := fi.src.offset
	stmt, err := fi.begin()
	if err != nil {
		return err
	}
	defer func() {
		if fi.tx != nil {
			fi.tx.Rollback()
		}
	}()
	pending := 0
	for {
		line, rerr := br.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return rerr
		}
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		if !complete && !(rerr == io.EOF && fi.lf.compressed && len(line) > 0) {
			// Partial last line of a file still being written: leave it for
			// the next run.
			break
		}
		lineOff := offset
		offset += int64(len(line))
		text := line
		if complete {
			text = line[:len(line)-1]
		}
		if n := len(text); n > 0 && text[n-1] == '\r' {
			text = text[:n-1]
		}
		if err := fi.handle(stmt, text, lineOff); err != nil {
			return err
		}
		pending++
		if pending >= batchLines {
			if err := fi.commit(offset, false); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if stmt, err = fi.begin(); err != nil {
				return err
			}
			pending = 0
		}
		if rerr == io.EOF {
			break
		}
	}
	// A compressed file is immutable: once read to the end it is complete.
	return fi.commit(offset, fi.lf.compressed)
}

func (fi *fileImport) begin() (*sql.Stmt, error) {
	tx, err := fi.im.DB.Begin()
	if err != nil {
		return nil, err
	}
	stmt, err := tx.Prepare(insertEvent)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	fi.tx = tx
	return stmt, nil
}

// handle parses and stores one line.
func (fi *fileImport) handle(stmt *sql.Stmt, text string, off int64) error {
	fi.fr.Lines++
	ev, err := logparse.Parse(text, fi.clock)
	switch {
	case errors.Is(err, logparse.ErrNotPostfix):
		fi.fr.NonPostfix++
		return nil
	case err != nil:
		fi.fr.Malformed++
		return nil
	}
	if fi.hosts != nil && !fi.hosts[ev.Host] {
		fi.fr.NonPostfix++
		return nil
	}
	if fi.fr.FirstTS == 0 || ev.TS < fi.fr.FirstTS {
		fi.fr.FirstTS = ev.TS
	}
	if ev.TS > fi.fr.LastTS {
		fi.fr.LastTS = ev.TS
	}
	fi.trackSegment(ev.Host, ev.TS)
	fi.markDay(ev.TS)
	if ev.Kind == logparse.KindUnknown {
		fi.fr.Unknown++
		u := fi.unknown[ev.Proc]
		if u == nil {
			u = &unknownAgg{sample: text}
			fi.unknown[ev.Proc] = u
		}
		u.count++
		return nil
	}
	if !ev.Kind.Stored() {
		return nil
	}
	key := fi.ukey(ev.TS, text)
	r, err := stmt.Exec(fi.src.id, off, key, ev.TS, ev.Host, ev.Inst, ev.Proc, ev.PID, store.NullStr(ev.QID), string(ev.Kind),
		nz(ev.Sender, ev.Kind == logparse.KindActive || ev.Kind == logparse.KindPickup || ev.Kind == logparse.KindExpired),
		store.NullStr(ev.Rcpt), store.NullStr(ev.Orig), store.NullStr(ev.Client), store.NullStr(ev.IP), store.NullStr(ev.Relay),
		nullInt(ev.Size), nullInt(int64(ev.Nrcpt)), nullDelay(ev), store.NullStr(ev.Delays), store.NullStr(ev.DSN),
		store.NullStr(ev.Status), nullInt(int64(ev.Code)), store.NullStr(ev.Stage), textValue(ev),
		store.NullStr(ev.Method), store.NullStr(ev.User), nullUID(ev.UID), store.NullStr(ev.NewQID),
		nullInt(int64(ev.TLS)), nullInt(int64(ev.AuthOK)), nullInt(int64(ev.AuthKO)))
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		fi.fr.Duplicates++
		return nil
	}
	fi.fr.Events++
	if m, ok := fi.res.MinTS[ev.Host]; !ok || ev.TS < m {
		fi.res.MinTS[ev.Host] = ev.TS
	}
	return nil
}

// nz keeps "" (the null sender <>) for fields where it is meaningful.
func nz(s string, keepEmpty bool) any {
	if keepEmpty {
		return s
	}
	return store.NullStr(s)
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullUID(v int) any {
	if v < 0 {
		return nil
	}
	return v
}

func nullDelay(ev logparse.Event) any {
	if ev.Kind != logparse.KindDelivery {
		return nil
	}
	return ev.Delay
}

// textValue stores the message-id even when empty ("<>"), other texts only if set.
func textValue(ev logparse.Event) any {
	if ev.Kind == logparse.KindMessageID {
		return ev.Text
	}
	return store.NullStr(ev.Text)
}

// ukey computes the duplicate key: hash of the raw line plus its occurrence
// number among identical lines in the same second.
func (fi *fileImport) ukey(ts int64, text string) int64 {
	h := fnv.New64a()
	h.Write([]byte(text))
	base := h.Sum64()
	t := &fi.src.tail
	if t.TS != ts {
		t.TS = ts
		t.cnt = map[uint64]int{}
	}
	occ := t.cnt[base]
	t.cnt[base] = occ + 1
	h.Write([]byte{0, byte(occ), byte(occ >> 8), byte(occ >> 16)})
	return int64(h.Sum64() >> 1)
}

func (fi *fileImport) trackSegment(host string, ts int64) {
	s := fi.segs[host]
	if s == nil {
		s = &segState{host: host, start: ts, end: ts}
		// Continue the last stored segment of this source if contiguous.
		var id, start, end int64
		err := fi.tx.QueryRow(`SELECT id, start_ts, end_ts FROM segments WHERE source_id=? AND host=? ORDER BY end_ts DESC LIMIT 1`,
			fi.src.id, host).Scan(&id, &start, &end)
		if err == nil && ts >= start && ts-end <= fi.gap {
			s.id, s.start, s.end = id, start, end
		}
		fi.segs[host] = s
	}
	switch {
	case ts-s.end > fi.gap:
		fi.closed = append(fi.closed, *s)
		*s = segState{host: host, start: ts, end: ts}
	case ts > s.end:
		s.end = ts
	}
}

func (fi *fileImport) markDay(ts int64) {
	if ts >= fi.dayStart && ts < fi.dayEnd {
		return
	}
	p := period.Of(period.Day, time.Unix(ts, 0), fi.im.Cfg.Location)
	fi.dayStart, fi.dayEnd = p.Start.Unix(), p.End.Unix()
	fi.res.DirtyDays[p.Key] = true
}

// commit persists the checkpoint, segments and counters with the events.
func (fi *fileImport) commit(offset int64, complete bool) error {
	tx := fi.tx
	fi.tx = nil
	save := func(host string, s *segState) error {
		if s.id == 0 {
			r, err := tx.Exec(`INSERT INTO segments(source_id, host, start_ts, end_ts) VALUES(?,?,?,?)`, fi.src.id, host, s.start, s.end)
			if err != nil {
				return err
			}
			s.id, _ = r.LastInsertId()
			return nil
		}
		_, err := tx.Exec(`UPDATE segments SET end_ts=? WHERE id=?`, s.end, s.id)
		return err
	}
	for i := range fi.closed {
		if err := save(fi.closed[i].host, &fi.closed[i]); err != nil {
			tx.Rollback()
			return err
		}
	}
	fi.closed = nil
	for host, s := range fi.segs {
		if err := save(host, s); err != nil {
			tx.Rollback()
			return err
		}
	}
	for proc, u := range fi.unknown {
		if _, err := tx.Exec(`INSERT INTO unknown_lines(source_id, proc, count, sample) VALUES(?,?,?,?)
			ON CONFLICT(source_id, proc) DO UPDATE SET count = count + excluded.count`, fi.src.id, proc, u.count, u.sample); err != nil {
			tx.Rollback()
			return err
		}
	}
	fi.unknown = map[string]*unknownAgg{}
	cs, _ := json.Marshal(fi.clock.State())
	first := fi.src.firstTS
	if fi.fr.FirstTS != 0 && (!first.Valid || fi.fr.FirstTS < first.Int64) {
		first = sql.NullInt64{Int64: fi.fr.FirstTS, Valid: true}
	}
	last := fi.src.lastTS
	if fi.fr.LastTS > last.Int64 {
		last = sql.NullInt64{Int64: fi.fr.LastTS, Valid: true}
	}
	fi.src.firstTS, fi.src.lastTS = first, last
	lines := fi.src.lines + fi.fr.Lines
	if _, err := tx.Exec(`UPDATE sources SET offset=?, lines=?, first_ts=?, last_ts=?, clock_state=?, tail_state=?,
		compressed=?, file_size=?, file_mtime=?, complete=?, updated_at=? WHERE id=?`,
		offset, lines, first, last, string(cs), fi.src.tail.dump(), fi.lf.compressed, fi.lf.size, fi.lf.mtime,
		complete, time.Now().Unix(), fi.src.id); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fi.fr.EndOff = offset
	return nil
}

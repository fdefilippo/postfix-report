// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package correlate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/logparse"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
)

// chunkSize is the number of events processed per transaction.
const chunkSize = 20000

// Correlator correlates stored events into instances, recipients, deliveries,
// rejects and security events.
type Correlator struct {
	DB  *store.DB
	Cfg *config.Config
	Log *slog.Logger
}

// Stats summarizes a correlation run.
type Stats struct {
	Events     int64
	FullHosts  []string // hosts re-correlated from scratch
	DirtyDays  map[string]bool
	Reinjected int64 // reinjections linked to their original
	Orphans    int64 // reinjections without original
}

type dbEvent struct {
	id int64
	logparse.Event
}

// hostState is the in-memory correlation state of one host.
type hostState struct {
	c       *Correlator
	tx      *sql.Tx
	host    string
	open    map[string]*instance
	cands   []*candidate
	pending map[string]pendingNotif // new qid -> notification announced before the instance
	touched map[*instance]bool
	stats   *Stats
	lastTS  int64
	stmts   map[string]*sql.Stmt // prepared statements of the current transaction
}

// exec runs a statement in the current transaction, preparing it once per
// transaction.
func (hs *hostState) exec(query string, args ...any) (sql.Result, error) {
	st := hs.stmts[query]
	if st == nil {
		var err error
		if st, err = hs.tx.Prepare(query); err != nil {
			return nil, err
		}
		hs.stmts[query] = st
	}
	return st.Exec(args...)
}

type pendingNotif struct {
	typ      string
	parentID int64
	ts       int64
}

// Run correlates the events not yet processed. minTS gives, per host, the
// earliest newly imported event: if it precedes the host watermark the host
// is re-correlated from scratch.
func (c *Correlator) Run(ctx context.Context, minTS map[string]int64) (*Stats, error) {
	st := &Stats{DirtyDays: map[string]bool{}}
	hosts, err := c.hosts()
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		wmTS, wmID, err := c.watermark(h)
		if err != nil {
			return nil, err
		}
		if m, ok := minTS[h]; ok && wmTS > 0 && m < wmTS {
			c.Log.Warn("events older than the correlation watermark: re-correlating host",
				"host", h, "oldest_new", time.Unix(m, 0), "watermark", time.Unix(wmTS, 0))
			if err := c.reset(ctx, h, st); err != nil {
				return nil, err
			}
			st.FullHosts = append(st.FullHosts, h)
			wmTS, wmID = 0, 0
		}
		if err := c.runHost(ctx, h, wmTS, wmID, st); err != nil {
			return nil, fmt.Errorf("host %s: %w", h, err)
		}
	}
	return st, nil
}

// Rebuild discards all derived data and re-correlates every host.
func (c *Correlator) Rebuild(ctx context.Context) (*Stats, error) {
	st := &Stats{DirtyDays: map[string]bool{}}
	hosts, err := c.hosts()
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		if err := c.reset(ctx, h, st); err != nil {
			return nil, err
		}
		st.FullHosts = append(st.FullHosts, h)
		if err := c.runHost(ctx, h, 0, 0, st); err != nil {
			return nil, fmt.Errorf("host %s: %w", h, err)
		}
	}
	return st, nil
}

func (c *Correlator) hosts() ([]string, error) {
	rows, err := c.DB.Query(`SELECT DISTINCT host FROM segments ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (c *Correlator) watermark(host string) (int64, int64, error) {
	var ts, id int64
	err := c.DB.QueryRow(`SELECT wm_ts, wm_id FROM corr_state WHERE host=?`, host).Scan(&ts, &id)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return ts, id, err
}

// reset deletes the derived data of a host and marks all its days dirty.
func (c *Correlator) reset(ctx context.Context, host string, st *Stats) error {
	return c.DB.Tx(ctx, func(tx *sql.Tx) error {
		stmts := []string{
			`DELETE FROM deliveries WHERE instance_id IN (SELECT id FROM instances WHERE host=?)`,
			`DELETE FROM recipients WHERE instance_id IN (SELECT id FROM instances WHERE host=?)`,
			`DELETE FROM rejects WHERE host=?`,
			`DELETE FROM security_events WHERE host=?`,
			`UPDATE instances SET parent_id=NULL, root_id=NULL, notif_parent_id=NULL WHERE host=?`,
			`DELETE FROM instances WHERE host=?`,
			`DELETE FROM corr_state WHERE host=?`,
		}
		for _, s := range stmts {
			if _, err := tx.Exec(s, host); err != nil {
				return err
			}
		}
		var lo, hi sql.NullInt64
		if err := tx.QueryRow(`SELECT MIN(start_ts), MAX(end_ts) FROM segments WHERE host=?`, host).Scan(&lo, &hi); err != nil {
			return err
		}
		if lo.Valid {
			for _, d := range daysBetween(lo.Int64, hi.Int64, c.Cfg.Location) {
				st.DirtyDays[d] = true
			}
		}
		return nil
	})
}

func daysBetween(lo, hi int64, loc *time.Location) []string {
	var out []string
	p := period.Of(period.Day, time.Unix(lo, 0), loc)
	for p.Start.Unix() <= hi {
		out = append(out, p.Key)
		p = p.Next()
	}
	return out
}

func (c *Correlator) runHost(ctx context.Context, host string, wmTS, wmID int64, st *Stats) error {
	hs := &hostState{c: c, host: host, pending: map[string]pendingNotif{}, stats: st}
	if err := hs.load(wmTS); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		evs, err := c.fetch(host, wmTS, wmID)
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			break
		}
		tx, err := c.DB.Begin()
		if err != nil {
			return err
		}
		hs.tx = tx
		hs.stmts = map[string]*sql.Stmt{}
		hs.touched = map[*instance]bool{}
		for i := range evs {
			if err := hs.apply(&evs[i]); err != nil {
				tx.Rollback()
				return fmt.Errorf("event %d: %w", evs[i].id, err)
			}
		}
		// The chunk ends on a complete second: every event up to wmTS is
		// processed; new events of that second will have a larger id.
		wmTS = evs[len(evs)-1].TS
		for _, e := range evs {
			if e.id > wmID {
				wmID = e.id
			}
		}
		more := len(evs) >= chunkSize
		if !more {
			// End of data: close instances silent for longer than stale_after.
			hs.expireStale(wmTS)
		}
		if err := hs.flush(); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(`INSERT INTO corr_state(host, wm_ts, wm_id) VALUES(?,?,?)
			ON CONFLICT(host) DO UPDATE SET wm_ts=excluded.wm_ts, wm_id=excluded.wm_id`, host, wmTS, wmID); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		st.Events += int64(len(evs))
		if !more {
			break
		}
	}
	return nil
}

const eventCols = `e.id, e.ts, e.host, e.inst, e.proc, COALESCE(e.pid,0), COALESCE(e.qid,''), e.kind, e.sender,
	COALESCE(e.rcpt,''), COALESCE(e.orig,''), COALESCE(e.client,''), COALESCE(e.ip,''), COALESCE(e.relay,''),
	COALESCE(e.size,0), COALESCE(e.nrcpt,0), COALESCE(e.delay,0), COALESCE(e.dsn,''), COALESCE(e.status,''),
	COALESCE(e.code,0), COALESCE(e.stage,''), e.text, COALESCE(e.method,''), COALESCE(e.user,''), e.uid,
	COALESCE(e.new_qid,''), COALESCE(e.tls,0), COALESCE(e.auth_ok,0), COALESCE(e.auth_ko,0)`

// fetch returns the next unprocessed events of a host, ordered by time,
// then by source age and file position (so that lines of the same second
// keep their order even when files are imported out of order). The batch
// always contains whole seconds.
func (c *Correlator) fetch(host string, wmTS, wmID int64) ([]dbEvent, error) {
	const cond = `e.host=? AND (e.ts > ? OR (e.ts = ? AND e.id > ?))`
	var last sql.NullInt64
	if err := c.DB.QueryRow(`SELECT MAX(ts) FROM (SELECT e.ts FROM events e WHERE `+cond+` ORDER BY e.ts LIMIT ?)`,
		host, wmTS, wmTS, wmID, chunkSize).Scan(&last); err != nil {
		return nil, err
	}
	if !last.Valid {
		return nil, nil
	}
	rows, err := c.DB.Query(`SELECT `+eventCols+` FROM events e JOIN sources s ON s.id = e.source_id
		WHERE `+cond+` AND e.ts <= ? ORDER BY e.ts, s.first_ts, e.source_id, e.line_offset, e.id`,
		host, wmTS, wmTS, wmID, last.Int64)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dbEvent
	for rows.Next() {
		var e dbEvent
		var kind string
		var sender, text sql.NullString
		var uid sql.NullInt64
		if err := rows.Scan(&e.id, &e.TS, &e.Host, &e.Inst, &e.Proc, &e.PID, &e.QID, &kind, &sender, &e.Rcpt, &e.Orig,
			&e.Client, &e.IP, &e.Relay, &e.Size, &e.Nrcpt, &e.Delay, &e.DSN, &e.Status, &e.Code, &e.Stage, &text,
			&e.Method, &e.User, &uid, &e.NewQID, &e.TLS, &e.AuthOK, &e.AuthKO); err != nil {
			return nil, err
		}
		e.Kind = logparse.Kind(kind)
		e.Sender = sender.String
		e.Text = text.String
		e.UID = -1
		if uid.Valid {
			e.UID = int(uid.Int64)
		}
		// Keep the "sender present" information: the null sender is "".
		if !sender.Valid {
			e.Sender = "\x00"
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// load restores open instances and reinjection candidates from the database.
func (hs *hostState) load(wmTS int64) error {
	hs.open = map[string]*instance{}
	db := hs.c.DB
	rows, err := db.Query(`SELECT `+instCols+` FROM instances WHERE host=? AND state='open'`, hs.host)
	if err != nil {
		return err
	}
	byID := map[int64]*instance{}
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			rows.Close()
			return err
		}
		hs.open[in.key()] = in
		byID[in.id] = in
	}
	rows.Close()
	if len(byID) > 0 {
		rr, err := db.Query(`SELECT r.id, r.instance_id, r.rcpt, r.orig, r.status, COALESCE(r.first_ts,0), COALESCE(r.final_ts,0),
			r.attempts, r.deferrals, r.final_delay, COALESCE(r.final_dsn,''), COALESCE(r.final_agent,'')
			FROM recipients r JOIN instances i ON i.id = r.instance_id WHERE i.host=? AND i.state='open'`, hs.host)
		if err != nil {
			return err
		}
		for rr.Next() {
			r := &recipient{}
			var iid int64
			if err := rr.Scan(&r.id, &iid, &r.rcpt, &r.orig, &r.status, &r.firstTS, &r.finalTS, &r.attempts, &r.deferrals,
				&r.finalDelay, &r.finalDSN, &r.finalAgent); err != nil {
				rr.Close()
				return err
			}
			if in := byID[iid]; in != nil {
				in.rcpts[rkey(r.rcpt, r.orig)] = r
			}
		}
		rr.Close()
	}
	win := int64(hs.c.Cfg.Correlation.ReinjectWindow.Seconds())
	cr, err := db.Query(`SELECT i.id, COALESCE(i.root_id, i.id), COALESCE(i.sender,''), COALESCE(i.nrcpt,0),
		COALESCE(i.message_id,''), i.has_message_id, i.accepted_ts
		FROM instances i WHERE i.host=? AND i.origin IN ('smtp','local') AND i.accepted_ts >= ?
		AND NOT EXISTS (SELECT 1 FROM instances c WHERE c.parent_id = i.id) ORDER BY i.accepted_ts, i.id`,
		hs.host, wmTS-win)
	if err != nil {
		return err
	}
	defer cr.Close()
	for cr.Next() {
		cd := &candidate{}
		if err := cr.Scan(&cd.id, &cd.rootID, &cd.sender, &cd.nrcpt, &cd.messageID, &cd.hasMessageID, &cd.acceptedTS); err != nil {
			return err
		}
		hs.cands = append(hs.cands, cd)
	}
	return cr.Err()
}

func (hs *hostState) cfg() *config.Config { return hs.c.Cfg }

func (hs *hostState) touch(in *instance, ts int64) {
	in.dirty = true
	if ts > in.lastTS {
		in.lastTS = ts
	}
	hs.touched[in] = true
}

// newInstance creates and stores a new open instance.
func (hs *hostState) newInstance(e *dbEvent, origin string) (*instance, error) {
	in := &instance{
		host: e.Host, inst: e.Inst, qid: e.QID, origin: origin, state: StateOpen,
		firstTS: e.TS, lastTS: e.TS, uid: -1, rcpts: map[string]*recipient{},
	}
	r, err := hs.exec(`INSERT INTO instances(host, inst, qid, origin, state, first_ts, last_ts) VALUES(?,?,?,?,?,?,?)`,
		in.host, in.inst, in.qid, in.origin, in.state, in.firstTS, in.lastTS)
	if err != nil {
		return nil, err
	}
	in.id, _ = r.LastInsertId()
	in.rootID = in.id
	hs.open[in.key()] = in
	hs.touch(in, e.TS)
	return in, nil
}

// close removes an instance from the open set.
func (hs *hostState) close(in *instance, state string, ts int64) {
	in.state = state
	if state == StateRemoved {
		in.removedTS = ts
	}
	delete(hs.open, in.key())
	hs.touch(in, ts)
}

// startInstance returns a fresh instance for a lifecycle-starting event,
// superseding a previous open instance with the same Queue ID.
func (hs *hostState) startInstance(e *dbEvent, origin string) (*instance, error) {
	if cur := hs.open[e.Inst+"/"+e.QID]; cur != nil {
		// The Queue ID is in use again but the previous lifecycle never
		// logged "removed" (lost lines or a gap in the logs).
		cur.flag(FlagQIDReuse)
		hs.close(cur, StateSuperseded, e.TS)
	}
	return hs.newInstance(e, origin)
}

// getInstance returns the open instance for e, creating one whose start was
// not observed if needed.
func (hs *hostState) getInstance(e *dbEvent) (*instance, error) {
	if cur := hs.open[e.Inst+"/"+e.QID]; cur != nil {
		return cur, nil
	}
	in, err := hs.newInstance(e, OriginUnknown)
	if err != nil {
		return nil, err
	}
	in.flag(FlagNoStart)
	return in, nil
}

func (hs *hostState) apply(e *dbEvent) error {
	hs.lastTS = e.TS
	switch e.Kind {
	case logparse.KindClient:
		in, err := hs.startInstance(e, OriginSMTP)
		if err != nil {
			return err
		}
		in.hasStart = true
		in.client, in.ip, in.saslMethod, in.saslUser = e.Client, e.IP, e.Method, e.User
	case logparse.KindPickup:
		origin := OriginLocal
		if hs.cfg().IsReinjectUID(e.UID) {
			origin = OriginReinject
		}
		in, err := hs.startInstance(e, origin)
		if err != nil {
			return err
		}
		in.hasStart = true
		in.uid = e.UID
		if e.Sender != "\x00" {
			in.sender, in.hasSender = e.Sender, true
		}
	case logparse.KindMessageID:
		cur := hs.open[e.Inst+"/"+e.QID]
		if cur == nil || (cur.hasMessageID && cur.acceptedTS != 0) {
			var err error
			if cur, err = hs.startInstance(e, OriginInternal); err != nil {
				return err
			}
			if p, ok := hs.pending[e.QID]; ok && e.TS-p.ts <= 60 {
				cur.origin, cur.notifType, cur.notifParentID = OriginNotification, p.typ, p.parentID
				delete(hs.pending, e.QID)
			}
		}
		cur.messageID, cur.hasMessageID = e.Text, true
		hs.touch(cur, e.TS)
	case logparse.KindActive:
		return hs.active(e)
	case logparse.KindDelivery:
		return hs.delivery(e)
	case logparse.KindExpired:
		in, err := hs.getInstance(e)
		if err != nil {
			return err
		}
		in.expiredTS = e.TS
		n := 0
		for _, r := range in.rcpts {
			if r.status == RcptPending {
				r.status, r.finalTS, r.dirty = RcptExpired, e.TS, true
				n++
			}
		}
		if n == 0 {
			in.flag(FlagExpiredNoRcpt)
		}
		hs.touch(in, e.TS)
	case logparse.KindNotification:
		parent, err := hs.getInstance(e)
		if err != nil {
			return err
		}
		hs.touch(parent, e.TS)
		if child := hs.open[e.Inst+"/"+e.NewQID]; child != nil {
			child.origin, child.notifType, child.notifParentID = OriginNotification, e.Status, parent.id
			hs.touch(child, e.TS)
		} else {
			hs.pending[e.NewQID] = pendingNotif{typ: e.Status, parentID: parent.id, ts: e.TS}
		}
	case logparse.KindRemoved:
		in := hs.open[e.Inst+"/"+e.QID]
		if in == nil {
			// "removed" without any other line (start before the data):
			// nothing to count, keep the lifecycle for completeness.
			var err error
			if in, err = hs.getInstance(e); err != nil {
				return err
			}
		}
		hs.close(in, StateRemoved, e.TS)
	case logparse.KindReject, logparse.KindDiscard, logparse.KindHold:
		return hs.reject(e)
	case logparse.KindSASLFail, logparse.KindTLSError, logparse.KindSessionAbort:
		detail := e.Text
		if e.Kind == logparse.KindSessionAbort {
			detail = e.Text + " after " + e.Stage
		}
		_, err := hs.exec(`INSERT OR IGNORE INTO security_events(event_id, ts, host, kind, ip, client, method, detail) VALUES(?,?,?,?,?,?,?,?)`,
			e.id, e.TS, e.Host, string(e.Kind), nullStr(e.IP), nullStr(e.Client), nullStr(e.Method), nullStr(detail))
		return err
	}
	return nil
}

func (hs *hostState) active(e *dbEvent) error {
	in, err := hs.getInstance(e)
	if err != nil {
		return err
	}
	hs.touch(in, e.TS)
	if in.acceptedTS != 0 {
		return nil // retry from the deferred queue
	}
	in.acceptedTS = e.TS
	// Acceptance is observed only if the lifecycle start was seen.
	in.acceptObserved = in.hasStart || in.hasMessageID
	if e.Sender != "\x00" {
		in.sender, in.hasSender = e.Sender, true
	}
	in.size, in.nrcpt = e.Size, e.Nrcpt
	switch in.origin {
	case OriginSMTP, OriginLocal:
		if len(hs.cands) > 4096 {
			hs.pruneCandidates(e.TS)
		}
		hs.cands = append(hs.cands, &candidate{id: in.id, rootID: in.rootID, sender: in.sender, nrcpt: in.nrcpt,
			messageID: in.messageID, hasMessageID: in.hasMessageID, acceptedTS: in.acceptedTS})
	case OriginReinject:
		hs.link(in)
	}
	return nil
}

// link attaches a reinjected instance to its original: same sender, nrcpt and
// message-id, accepted within the reinjection window; the oldest unlinked
// candidate wins (the filter processes messages in order).
func (hs *hostState) link(in *instance) {
	hs.pruneCandidates(in.acceptedTS)
	for _, cd := range hs.cands {
		if cd.acceptedTS > in.acceptedTS || cd.sender != in.sender || cd.nrcpt != in.nrcpt ||
			cd.hasMessageID != in.hasMessageID || cd.messageID != in.messageID {
			continue
		}
		cd.linked = true
		in.parentID, in.rootID = cd.id, cd.rootID
		hs.stats.Reinjected++
		return
	}
	in.flag(FlagReinjectOrphan)
	hs.stats.Orphans++
}

// pruneCandidates drops linked candidates and those too old to match a
// reinjection accepted at now or later.
func (hs *hostState) pruneCandidates(now int64) {
	win := int64(hs.cfg().Correlation.ReinjectWindow.Seconds())
	keep := hs.cands[:0]
	for _, cd := range hs.cands {
		if !cd.linked && cd.acceptedTS >= now-win {
			keep = append(keep, cd)
		}
	}
	for i := len(keep); i < len(hs.cands); i++ {
		hs.cands[i] = nil
	}
	hs.cands = keep
}

func (hs *hostState) delivery(e *dbEvent) error {
	in, err := hs.getInstance(e)
	if err != nil {
		return err
	}
	if in.acceptedTS == 0 {
		in.flag(FlagDeliveryNoQueue)
	}
	class := ClassDelivery
	switch {
	case e.Status == "deliverable" || e.Status == "undeliverable":
		class = ClassProbe
		if in.origin == OriginInternal || in.origin == OriginUnknown {
			in.origin = OriginProbe
		}
	case hs.cfg().IsFilterTransport(e.Relay):
		class = ClassFilter
		in.filtered = true
	}
	k := rkey(e.Rcpt, e.Orig)
	r := in.rcpts[k]
	if r == nil {
		r = &recipient{rcpt: e.Rcpt, orig: e.Orig, status: RcptPending, firstTS: e.TS}
		res, err := hs.exec(`INSERT INTO recipients(instance_id, rcpt, orig, status, first_ts) VALUES(?,?,?,?,?)`,
			in.id, r.rcpt, r.orig, r.status, r.firstTS)
		if err != nil {
			return err
		}
		r.id, _ = res.LastInsertId()
		in.rcpts[k] = r
	}
	afterDefer := false
	switch class {
	case ClassProbe:
		r.status, r.finalTS = RcptProbe, e.TS
	case ClassFilter:
		if e.Status == "sent" {
			r.status, r.finalTS = RcptFiltered, e.TS
			r.finalDelay = sql.NullFloat64{Float64: e.Delay, Valid: true}
			r.finalDSN, r.finalAgent = e.DSN, e.Proc
		}
	default:
		r.attempts++
		switch e.Status {
		case "deferred":
			r.deferrals++
		case "sent", "bounced":
			afterDefer = r.deferrals > 0
			if r.status == RcptPending {
				r.status = RcptDelivered
				if e.Status == "bounced" {
					r.status = RcptFailed
				}
				if e.Proc == "discard" {
					r.status = RcptDiscarded
				}
				r.finalTS = e.TS
				r.finalDelay = sql.NullFloat64{Float64: e.Delay, Valid: true}
				r.finalDSN, r.finalAgent = e.DSN, e.Proc
			}
		}
	}
	r.dirty = true
	hs.touch(in, e.TS)
	_, err = hs.exec(`INSERT OR IGNORE INTO deliveries(event_id, instance_id, recipient_id, ts, agent, rcpt, relay, delay, dsn, status, text, class, after_defer)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.id, in.id, r.id, e.TS, e.Proc, e.Rcpt, nullStr(e.Relay), e.Delay,
		nullStr(e.DSN), e.Status, nullStr(e.Text), class, afterDefer)
	return err
}

func (hs *hostState) reject(e *dbEvent) error {
	var instID any
	if e.QID != "" {
		in, err := hs.getInstance(e)
		if err != nil {
			return err
		}
		instID = in.id
		hs.touch(in, e.TS)
		// Rejections in cleanup (milter, header/body checks) or at the end of
		// DATA end the transaction; an smtpd reject of one RCPT does not.
		ends := e.Proc == "cleanup" || e.Stage == "END-OF-MESSAGE" || e.Stage == "DATA" ||
			e.Stage == "header" || e.Stage == "body"
		switch {
		case e.Kind == logparse.KindHold:
			in.flag(FlagHold)
		case ends && in.acceptedTS == 0 && e.Kind == logparse.KindDiscard:
			hs.close(in, StateDiscarded, e.TS)
		case ends && in.acceptedTS == 0:
			hs.close(in, StateRejected, e.TS)
		}
	}
	if e.Kind == logparse.KindHold {
		return nil
	}
	action := e.Status
	if action == "" {
		action = string(e.Kind)
	}
	_, err := hs.exec(`INSERT OR IGNORE INTO rejects(event_id, instance_id, ts, host, action, source, stage, code, dsn, reason, client, ip, sender, rcpt)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.id, instID, e.TS, e.Host, action, e.Proc, nullStr(e.Stage), nullInt(int64(e.Code)),
		nullStr(e.DSN), nullStr(e.Text), nullStr(e.Client), nullStr(e.IP), senderValue(e.Sender), nullStr(e.Orig))
	return err
}

func senderValue(s string) any {
	if s == "\x00" {
		return nil
	}
	return s
}

// expireStale closes instances without events for longer than stale_after.
func (hs *hostState) expireStale(now int64) {
	limit := now - int64(hs.cfg().Correlation.StaleAfter.Seconds())
	var keys []string
	for k, in := range hs.open {
		if in.lastTS < limit {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		in := hs.open[k]
		in.flag(FlagStale)
		hs.close(in, StateStale, in.lastTS)
	}
}

// flush writes the touched instances and recipients and records dirty days.
func (hs *hostState) flush() error {
	loc := hs.cfg().Location
	for in := range hs.touched {
		if in.acceptedTS != 0 {
			hs.stats.DirtyDays[period.DayKeyUnix(in.acceptedTS, loc)] = true
		}
		if err := hs.saveInstance(in); err != nil {
			return err
		}
		for _, r := range in.rcpts {
			if !r.dirty {
				continue
			}
			if _, err := hs.exec(`UPDATE recipients SET status=?, final_ts=?, attempts=?, deferrals=?, final_delay=?, final_dsn=?, final_agent=? WHERE id=?`,
				r.status, nullInt(r.finalTS), r.attempts, r.deferrals, r.finalDelay, nullStr(r.finalDSN), nullStr(r.finalAgent), r.id); err != nil {
				return err
			}
			if r.finalTS != 0 {
				hs.stats.DirtyDays[period.DayKeyUnix(r.finalTS, loc)] = true
			}
			r.dirty = false
		}
		in.dirty = false
	}
	return nil
}

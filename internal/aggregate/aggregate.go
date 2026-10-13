// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package aggregate computes hourly metrics and daily rankings from the
// correlated tables. A day is always recomputed as a whole, so aggregates
// can be rebuilt at any time without drift.
package aggregate

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/fdefilippo/postfix-report/internal/norm"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/store"
)

// Metric names stored in agg_hour. See METRICS.md for definitions.
const (
	MAccepted       = "msg_accepted"       // messages accepted (smtp + local)
	MAcceptedSMTP   = "msg_smtp"           // ... received via SMTP
	MAcceptedLocal  = "msg_local"          // ... originated locally
	MRecipients     = "rcpt_accepted"      // recipients of accepted messages
	MBytes          = "bytes_accepted"     // sum of size= of accepted messages
	MNotifications  = "notifications"      // Postfix-generated DSN/bounce messages
	MAttempts       = "attempts"           // delivery status lines (excl. filter, probes)
	MSent           = "sent"               // status=sent
	MSentNotif      = "sent_notification"  // ... of which deliveries of notifications
	MDeferred       = "deferred"           // status=deferred
	MDeferredError  = "deferred_suspended" // ... of which by the error agent (no network attempt)
	MBounced        = "bounced"            // status=bounced
	MExpiredRcpt    = "expired_rcpt"       // recipients failed by queue lifetime expiry
	MExpiredMsg     = "expired_msg"        // instances expired
	MFinalFailures  = "final_failures"     // bounced + expired_rcpt
	MSentAfterDefer = "sent_after_defer"   // sent after >= 1 deferral of the recipient
	MDelaySum       = "delay_sum"          // sum of delay= of sent deliveries
	MDelayCount     = "delay_count"        // number of sent deliveries with delay
	MRejects        = "rejects"            // SMTP rejections (all)
	MRejectsTemp    = "rejects_temp"       // 4xx
	MRejectsPerm    = "rejects_perm"       // 5xx
	MDiscarded      = "discarded"          // discard actions
	MFilterHandoff  = "filter_handoffs"    // deliveries to the content filter
	MConnections    = "connections"        // smtpd connect
	MSessionsTLS    = "sessions_starttls"  // disconnect with starttls >= 1
	MSessions       = "sessions"           // smtpd disconnect
	MSASLOK         = "sasl_ok"            // accepted messages with SASL auth
	MSASLFail       = "sasl_fail"          // SASL authentication failures
	MTLSError       = "tls_errors"         // TLS handshake errors
	MSessionAbort   = "session_aborts"     // lost connection / timeout
	MTLSIn          = "tls_in"             // logged inbound TLS handshakes
	MTLSOut         = "tls_out"            // logged outbound TLS handshakes
)

// Dimensions stored in agg_dim.
const (
	DSenderDomain = "sender_domain" // accepted messages
	DRcptDomain   = "rcpt_domain"   // sent deliveries
	DClientIP     = "client_ip"     // accepted SMTP messages
	DRejectReason = "reject_reason" // rejections
	DRejectIP     = "reject_ip"     // rejections
	DDeferReason  = "defer_reason"  // deferrals
	DBounceReason = "bounce_reason" // bounces
	DSASLUser     = "sasl_user"     // accepted messages
	DSASLFailIP   = "sasl_fail_ip"  // SASL failures
	DRelay        = "relay"         // sent deliveries
	DAgent        = "agent_status"  // delivery lines by agent/status
	DOrigin       = "origin"        // accepted instances by origin (incl. internal)
	DTLSProto     = "tls_protocol"  // logged TLS handshakes
)

// Aggregator recomputes aggregates.
type Aggregator struct {
	DB  *store.DB
	Loc *time.Location
}

type dayAcc struct {
	day  period.Period
	loc  *time.Location
	hour map[int]map[string]float64
	dim  map[string]map[string]int64
}

func (a *dayAcc) add(ts int64, metric string, v float64) {
	h := time.Unix(ts, 0).In(a.loc).Hour()
	m := a.hour[h]
	if m == nil {
		m = map[string]float64{}
		a.hour[h] = m
	}
	m[metric] += v
}

func (a *dayAcc) dimAdd(dim, key string, v int64) {
	m := a.dim[dim]
	if m == nil {
		m = map[string]int64{}
		a.dim[dim] = m
	}
	m[key] += v
}

// RecomputeDirty recomputes all days listed in dirty_days and clears them.
// It returns the recomputed day keys.
func (ag *Aggregator) RecomputeDirty(ctx context.Context) ([]string, error) {
	rows, err := ag.DB.Query(`SELECT day FROM dirty_days ORDER BY day`)
	if err != nil {
		return nil, err
	}
	var days []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return nil, err
		}
		days = append(days, d)
	}
	rows.Close()
	for _, d := range days {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := ag.RecomputeDay(ctx, d); err != nil {
			return nil, fmt.Errorf("day %s: %w", d, err)
		}
	}
	return days, nil
}

// compute accumulates the metrics of one local day from the base tables.
func (ag *Aggregator) compute(q queryer, day string) (*dayAcc, error) {
	p, err := period.Parse(period.Day, day, ag.Loc)
	if err != nil {
		return nil, err
	}
	acc := &dayAcc{day: p, loc: ag.Loc, hour: map[int]map[string]float64{}, dim: map[string]map[string]int64{}}
	lo, hi := p.Start.Unix(), p.End.Unix()
	for _, f := range []func(queryer, *dayAcc, int64, int64) error{
		accAccepted, accNotifications, accDeliveries, accExpired, accRejects, accSecurity, accSessions,
	} {
		if err := f(q, acc, lo, hi); err != nil {
			return nil, err
		}
	}
	return acc, nil
}

// queryer is satisfied by *sql.DB, *sql.Tx and *store.DB.
type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// RecomputeDay rebuilds agg_hour and agg_dim for one local day.
func (ag *Aggregator) RecomputeDay(ctx context.Context, day string) error {
	return ag.DB.Tx(ctx, func(tx *sql.Tx) error {
		acc, err := ag.compute(tx, day)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM agg_hour WHERE day=?`, day); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM agg_dim WHERE day=?`, day); err != nil {
			return err
		}
		hours := make([]int, 0, len(acc.hour))
		for h := range acc.hour {
			hours = append(hours, h)
		}
		sort.Ints(hours)
		for _, h := range hours {
			for m, v := range acc.hour[h] {
				if _, err := tx.Exec(`INSERT INTO agg_hour(day, hour, metric, value) VALUES(?,?,?,?)`, day, h, m, v); err != nil {
					return err
				}
			}
		}
		for d, m := range acc.dim {
			for k, v := range m {
				if _, err := tx.Exec(`INSERT INTO agg_dim(day, dim, key, value) VALUES(?,?,?,?)`, day, d, k, v); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(`DELETE FROM dirty_days WHERE day=?`, day)
		return err
	})
}

// Compare recomputes a day in memory and lists the differences with the
// stored aggregates (empty if consistent).
func (ag *Aggregator) Compare(day string) ([]string, error) {
	acc, err := ag.compute(ag.DB, day)
	if err != nil {
		return nil, err
	}
	stored := map[[2]any]float64{}
	rows, err := ag.DB.Query(`SELECT hour, metric, value FROM agg_hour WHERE day=?`, day)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h int
		var m string
		var v float64
		if err := rows.Scan(&h, &m, &v); err != nil {
			rows.Close()
			return nil, err
		}
		stored[[2]any{h, m}] = v
	}
	rows.Close()
	var diffs []string
	for h, ms := range acc.hour {
		for m, v := range ms {
			k := [2]any{h, m}
			if sv, ok := stored[k]; !ok || math.Abs(sv-v) > 1e-6*math.Max(1, math.Abs(v)) {
				diffs = append(diffs, fmt.Sprintf("%s %02d %s: stored %v, recomputed %v", day, h, m, sv, v))
			}
			delete(stored, k)
		}
	}
	for k, v := range stored {
		diffs = append(diffs, fmt.Sprintf("%s %02d %s: stored %v, recomputed 0", day, k[0], k[1], v))
	}
	var nDim, nStored int64
	for _, m := range acc.dim {
		for _, v := range m {
			nDim += v
		}
	}
	if err := ag.DB.QueryRow(`SELECT COALESCE(SUM(value),0) FROM agg_dim WHERE day=?`, day).Scan(&nStored); err != nil {
		return nil, err
	}
	if nDim != nStored {
		diffs = append(diffs, fmt.Sprintf("%s rankings: stored total %d, recomputed %d", day, nStored, nDim))
	}
	sort.Strings(diffs)
	return diffs, nil
}

func accAccepted(tx queryer, a *dayAcc, lo, hi int64) error {
	rows, err := tx.Query(`SELECT accepted_ts, origin, accept_observed, parent_id IS NULL, COALESCE(sender,''), COALESCE(size,0),
		COALESCE(nrcpt,0), COALESCE(ip,''), COALESCE(sasl_user,'')
		FROM instances WHERE accepted_ts >= ? AND accepted_ts < ?`, lo, hi)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts, size, nrcpt int64
		var origin, sender, ip, user string
		var observed, root bool
		if err := rows.Scan(&ts, &origin, &observed, &root, &sender, &size, &nrcpt, &ip, &user); err != nil {
			return err
		}
		label := origin
		if !observed {
			label += " (acceptance not observed)"
		}
		a.dimAdd(DOrigin, label, 1)
		if !observed || !root || (origin != "smtp" && origin != "local") {
			continue
		}
		a.add(ts, MAccepted, 1)
		a.add(ts, MRecipients, float64(nrcpt))
		a.add(ts, MBytes, float64(size))
		a.dimAdd(DSenderDomain, norm.Domain(sender), 1)
		if origin == "smtp" {
			a.add(ts, MAcceptedSMTP, 1)
			if ip != "" {
				a.dimAdd(DClientIP, ip, 1)
			}
			if user != "" {
				a.add(ts, MSASLOK, 1)
				a.dimAdd(DSASLUser, user, 1)
			}
		} else {
			a.add(ts, MAcceptedLocal, 1)
		}
	}
	return rows.Err()
}

func accNotifications(tx queryer, a *dayAcc, lo, hi int64) error {
	rows, err := tx.Query(`SELECT first_ts FROM instances WHERE origin='notification' AND first_ts >= ? AND first_ts < ?`, lo, hi)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts int64
		if err := rows.Scan(&ts); err != nil {
			return err
		}
		a.add(ts, MNotifications, 1)
	}
	return rows.Err()
}

func accDeliveries(tx queryer, a *dayAcc, lo, hi int64) error {
	rows, err := tx.Query(`SELECT d.ts, d.agent, d.rcpt, COALESCE(d.relay,''), d.delay, COALESCE(d.dsn,''), d.status,
		COALESCE(d.text,''), d.class, d.after_defer, i.origin
		FROM deliveries d JOIN instances i ON i.id = d.instance_id WHERE d.ts >= ? AND d.ts < ?`, lo, hi)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts int64
		var agent, rcpt, relay, dsn, status, text, class, origin string
		var delay float64
		var afterDefer bool
		if err := rows.Scan(&ts, &agent, &rcpt, &relay, &delay, &dsn, &status, &text, &class, &afterDefer, &origin); err != nil {
			return err
		}
		switch class {
		case "filter":
			a.add(ts, MFilterHandoff, 1)
			continue
		case "probe":
			continue
		}
		a.dimAdd(DAgent, agent+"/"+status, 1)
		switch status {
		case "sent":
			a.add(ts, MAttempts, 1)
			if agent == "discard" {
				a.add(ts, MDiscarded, 1)
				continue
			}
			a.add(ts, MSent, 1)
			a.add(ts, MDelaySum, delay)
			a.add(ts, MDelayCount, 1)
			if origin == "notification" {
				a.add(ts, MSentNotif, 1)
			}
			if afterDefer {
				a.add(ts, MSentAfterDefer, 1)
			}
			a.dimAdd(DRcptDomain, norm.Domain(rcpt), 1)
			a.dimAdd(DRelay, norm.RelayHost(relay), 1)
		case "deferred":
			a.add(ts, MAttempts, 1)
			a.add(ts, MDeferred, 1)
			if agent == "error" {
				a.add(ts, MDeferredError, 1)
			}
			a.dimAdd(DDeferReason, reasonKey(dsn, text), 1)
		case "bounced":
			a.add(ts, MAttempts, 1)
			a.add(ts, MBounced, 1)
			a.add(ts, MFinalFailures, 1)
			a.dimAdd(DBounceReason, reasonKey(dsn, text), 1)
		}
	}
	return rows.Err()
}

func reasonKey(dsn, text string) string {
	r := norm.Reason(text)
	if dsn != "" {
		return dsn + " " + r
	}
	return r
}

func accExpired(tx queryer, a *dayAcc, lo, hi int64) error {
	rows, err := tx.Query(`SELECT expired_ts FROM instances WHERE expired_ts >= ? AND expired_ts < ?`, lo, hi)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ts int64
		if err := rows.Scan(&ts); err != nil {
			rows.Close()
			return err
		}
		a.add(ts, MExpiredMsg, 1)
	}
	rows.Close()
	rows, err = tx.Query(`SELECT final_ts FROM recipients WHERE status='expired' AND final_ts >= ? AND final_ts < ?`, lo, hi)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts int64
		if err := rows.Scan(&ts); err != nil {
			return err
		}
		a.add(ts, MExpiredRcpt, 1)
		a.add(ts, MFinalFailures, 1)
	}
	return rows.Err()
}

func accRejects(tx queryer, a *dayAcc, lo, hi int64) error {
	rows, err := tx.Query(`SELECT ts, action, COALESCE(stage,''), COALESCE(code,0), COALESCE(dsn,''), COALESCE(reason,''), COALESCE(ip,'')
		FROM rejects WHERE ts >= ? AND ts < ?`, lo, hi)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts, code int64
		var action, stage, dsn, reason, ip string
		if err := rows.Scan(&ts, &action, &stage, &code, &dsn, &reason, &ip); err != nil {
			return err
		}
		if action == "discard" || action == "milter-discard" {
			a.add(ts, MDiscarded, 1)
			continue
		}
		a.add(ts, MRejects, 1)
		switch {
		case code >= 400 && code < 500:
			a.add(ts, MRejectsTemp, 1)
		case code >= 500:
			a.add(ts, MRejectsPerm, 1)
		}
		key := reasonKey(dsn, reason)
		if stage != "" {
			key = stage + ": " + key
		}
		a.dimAdd(DRejectReason, key, 1)
		if ip != "" {
			a.dimAdd(DRejectIP, ip, 1)
		}
	}
	return rows.Err()
}

func accSecurity(tx queryer, a *dayAcc, lo, hi int64) error {
	rows, err := tx.Query(`SELECT ts, kind, COALESCE(ip,'') FROM security_events WHERE ts >= ? AND ts < ?`, lo, hi)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts int64
		var kind, ip string
		if err := rows.Scan(&ts, &kind, &ip); err != nil {
			return err
		}
		switch kind {
		case "sasl_fail":
			a.add(ts, MSASLFail, 1)
			if ip != "" {
				a.dimAdd(DSASLFailIP, ip, 1)
			}
		case "tls_error":
			a.add(ts, MTLSError, 1)
		case "session_abort":
			a.add(ts, MSessionAbort, 1)
		}
	}
	return rows.Err()
}

func accSessions(tx queryer, a *dayAcc, lo, hi int64) error {
	rows, err := tx.Query(`SELECT ts, kind, proc, COALESCE(tls,0), COALESCE(text,'') FROM events
		WHERE ts >= ? AND ts < ? AND kind IN ('connect','disconnect','tls')`, lo, hi)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts, tls int64
		var kind, proc, text string
		if err := rows.Scan(&ts, &kind, &proc, &tls, &text); err != nil {
			return err
		}
		switch kind {
		case "connect":
			a.add(ts, MConnections, 1)
		case "disconnect":
			a.add(ts, MSessions, 1)
			if tls > 0 {
				a.add(ts, MSessionsTLS, 1)
			}
		case "tls":
			if proc == "smtpd" {
				a.add(ts, MTLSIn, 1)
			} else {
				a.add(ts, MTLSOut, 1)
			}
			a.dimAdd(DTLSProto, text, 1)
		}
	}
	return rows.Err()
}

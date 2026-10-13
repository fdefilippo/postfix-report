// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package correlate rebuilds message lifecycles from stored events.
//
// Events are processed per host in time order (ties: older source first,
// then file position). The state that must
// survive between runs (open queue instances, their recipients, reinjection
// candidates) lives in the database, so correlation is incremental and also
// works across files. If events older than the watermark arrive (backfill),
// the host is re-correlated from scratch.
package correlate

import (
	"database/sql"
	"strings"
)

// Origins of a queue instance.
const (
	OriginSMTP         = "smtp"
	OriginLocal        = "local"
	OriginReinject     = "reinject"
	OriginNotification = "notification"
	OriginProbe        = "probe"
	OriginInternal     = "internal"
	OriginUnknown      = "unknown"
)

// States of a queue instance.
const (
	StateOpen       = "open"
	StateRemoved    = "removed"
	StateRejected   = "rejected"
	StateDiscarded  = "discarded"
	StateSuperseded = "superseded"
	StateStale      = "stale"
)

// Recipient statuses.
const (
	RcptPending   = "pending"
	RcptDelivered = "delivered"
	RcptFailed    = "failed"
	RcptExpired   = "expired"
	RcptFiltered  = "filtered"
	RcptProbe     = "probe"
	RcptDiscarded = "discarded"
)

// Delivery classes.
const (
	ClassDelivery = "delivery"
	ClassFilter   = "filter"
	ClassProbe    = "probe"
)

// Anomaly flags.
const (
	FlagQIDReuse        = "qid_reused_before_removed"
	FlagNoStart         = "start_not_observed"
	FlagReinjectOrphan  = "reinject_without_original"
	FlagExpiredNoRcpt   = "expired_without_pending_rcpt"
	FlagHold            = "hold"
	FlagStale           = "stale"
	FlagDeliveryNoQueue = "delivery_before_active"
)

type instance struct {
	id             int64
	host, inst     string
	qid            string
	origin         string
	state          string
	firstTS        int64
	lastTS         int64
	acceptedTS     int64 // 0 = no "queue active" yet
	acceptObserved bool
	removedTS      int64
	sender         string
	hasSender      bool
	size           int64
	nrcpt          int
	messageID      string
	hasMessageID   bool
	client, ip     string
	saslMethod     string
	saslUser       string
	uid            int // -1 = none
	parentID       int64
	rootID         int64
	notifType      string
	notifParentID  int64
	filtered       bool
	expiredTS      int64
	flags          []string
	hasStart       bool // client= or pickup seen

	rcpts map[string]*recipient
	dirty bool
}

func (in *instance) key() string { return in.inst + "/" + in.qid }

func (in *instance) flag(f string) {
	for _, x := range in.flags {
		if x == f {
			return
		}
	}
	in.flags = append(in.flags, f)
	in.dirty = true
}

type recipient struct {
	id         int64
	rcpt, orig string
	status     string
	firstTS    int64
	finalTS    int64
	attempts   int
	deferrals  int
	finalDelay sql.NullFloat64
	finalDSN   string
	finalAgent string
	dirty      bool
}

func rkey(rcpt, orig string) string { return rcpt + "\x00" + orig }

// candidate is a root instance that may be the original of a reinjection.
type candidate struct {
	id, rootID   int64
	sender       string
	nrcpt        int
	messageID    string
	hasMessageID bool
	acceptedTS   int64
	linked       bool
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func joinFlags(f []string) string { return strings.Join(f, ",") }

func splitFlags(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

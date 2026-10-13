// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package logparse turns Postfix syslog lines into normalized events.
//
// The package is purely syntactic: it recognizes the line formats documented
// in LOG_ANALYSIS.md and extracts their fields. It never correlates lines and
// never assigns a meaning to formats it does not know (those become KindUnknown).
package logparse

// Kind classifies a normalized event.
type Kind string

const (
	KindConnect      Kind = "connect"       // smtpd connect from host[ip]
	KindDisconnect   Kind = "disconnect"    // smtpd disconnect from host[ip] <command stats>
	KindClient       Kind = "client"        // smtpd QID: client=host[ip][, sasl_method=, sasl_username=]
	KindReject       Kind = "reject"        // smtpd/cleanup [QID|NOQUEUE]: reject: / milter-reject:
	KindDiscard      Kind = "discard"       // smtpd/cleanup QID: discard: ...
	KindHold         Kind = "hold"          // cleanup QID: hold: ...
	KindSASLFail     Kind = "sasl_fail"     // smtpd warning: host[ip]: SASL X authentication failed: ...
	KindSessionAbort Kind = "session_abort" // smtpd lost connection / timeout after X from host[ip]
	KindTLSError     Kind = "tls_error"     // SSL_accept error / TLS library problem
	KindMessageID    Kind = "message_id"    // cleanup QID: message-id=<...>
	KindPickup       Kind = "pickup"        // pickup QID: uid=N from=<...>
	KindActive       Kind = "active"        // qmgr QID: from=<...>, size=N, nrcpt=N (queue active)
	KindRemoved      Kind = "removed"       // qmgr QID: removed
	KindExpired      Kind = "expired"       // qmgr QID: from=<...>, status=expired, returned to sender
	KindDelivery     Kind = "delivery"      // agent QID: to=<...>, relay=..., status=...
	KindNotification Kind = "notification"  // bounce QID: sender non-delivery notification: NEWQID
	KindHostReply    Kind = "host_reply"    // smtp QID: host X said: ... (per-MX diagnostic)
	KindServiceStart Kind = "service_start" // master daemon started
	KindServiceStop  Kind = "service_stop"  // master terminating on signal
	KindWarning      Kind = "warning"       // other warnings (counted, not stored)
	KindNoise        Kind = "noise"         // known lines without statistical value (not stored)
	KindUnknown      Kind = "unknown"       // unrecognized Postfix line (counted per process)
)

// Stored reports whether events of this kind are persisted in the database.
func (k Kind) Stored() bool {
	switch k {
	case KindWarning, KindNoise, KindUnknown:
		return false
	}
	return true
}

// Event is one normalized Postfix log line. Fields that do not apply to the
// kind are left at their zero value.
type Event struct {
	TS     int64  // Unix seconds (UTC)
	Host   string // syslog host
	Inst   string // syslog name of the Postfix instance ("postfix", "postfix-out")
	Proc   string // daemon ("smtpd", "qmgr", ...)
	PID    int
	QID    string
	Kind   Kind
	Raw    string // original line (not persisted)
	Sender string // from=
	Rcpt   string // to=
	Orig   string // orig_to= (delivery) or to= (reject) — see Kind
	Client string // client hostname
	IP     string // client IP
	Relay  string
	Size   int64
	Nrcpt  int
	Delay  float64
	Delays string
	DSN    string
	Status string // delivery status, or notification type
	Code   int    // SMTP reply code for rejects
	Stage  string // SMTP stage (RCPT, END-OF-MESSAGE, CONNECT, ...)
	Text   string // reason, response, message-id, ...
	Method string // SASL method
	User   string // SASL username
	UID    int    // pickup uid (-1 if absent)
	NewQID string // notification queue id
	TLS    int    // starttls count in disconnect
	AuthOK int    // successful AUTH commands in disconnect
	AuthKO int    // failed AUTH commands in disconnect
}

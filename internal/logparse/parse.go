// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrNotPostfix is returned for syslog lines that do not come from Postfix.
var ErrNotPostfix = errors.New("not a postfix line")

// ErrMalformed is returned for lines whose syslog header cannot be parsed.
var ErrMalformed = errors.New("malformed syslog line")

// Header is the syslog part of a line.
type Header struct {
	TS   time.Time
	Host string
	Inst string // syslog name, e.g. "postfix"
	Proc string // daemon name, e.g. "smtpd"
	PID  int
	Msg  string
}

// postfixDaemons lists the program names that identify a Postfix syslog tag
// ("<syslog_name>[/<service>]/<daemon>").
var postfixDaemons = map[string]bool{
	"smtpd": true, "smtp": true, "lmtp": true, "qmgr": true, "oqmgr": true, "cleanup": true,
	"pickup": true, "pipe": true, "local": true, "virtual": true, "bounce": true, "defer": true,
	"trace": true, "error": true, "retry": true, "discard": true, "anvil": true, "scache": true,
	"master": true, "postfix-script": true, "verify": true, "tlsmgr": true, "postscreen": true,
	"dnsblog": true, "trivial-rewrite": true, "showq": true, "postdrop": true, "sendmail": true,
	"postsuper": true, "postqueue": true, "proxymap": true, "tlsproxy": true, "flush": true,
	"spawn": true, "postlog": true, "postmulti": true, "smtpd-milter": true,
}

// ParseHeader parses the syslog header of a line. BSD timestamps are resolved
// with clock; RFC 3339 timestamps are absolute and only update the clock.
func ParseHeader(line string, clock *Clock) (Header, error) {
	var h Header
	var rest string
	if len(line) >= 16 && line[3] == ' ' && line[6] == ' ' && line[9] == ':' && line[12] == ':' && line[15] == ' ' {
		mon, ok := months[line[0:3]]
		if !ok {
			return h, ErrMalformed
		}
		day, err1 := strconv.Atoi(strings.TrimSpace(line[4:6]))
		hh, err2 := strconv.Atoi(line[7:9])
		mm, err3 := strconv.Atoi(line[10:12])
		ss, err4 := strconv.Atoi(line[13:15])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return h, ErrMalformed
		}
		rest = line[16:]
		// Parse the tag first: non-Postfix lines must not move the clock.
		if err := parseTag(rest, &h); err != nil {
			return h, err
		}
		t, err := clock.ResolveBSD(mon, day, hh, mm, ss)
		if err != nil {
			return h, ErrMalformed
		}
		h.TS = t
		return h, nil
	}
	if len(line) > 20 && line[4] == '-' && line[7] == '-' && line[10] == 'T' {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			return h, ErrMalformed
		}
		t, err := time.Parse(time.RFC3339Nano, line[:sp])
		if err != nil {
			return h, ErrMalformed
		}
		if err := parseTag(line[sp+1:], &h); err != nil {
			return h, err
		}
		t = t.Truncate(time.Second)
		clock.Observe(t)
		h.TS = t
		return h, nil
	}
	return h, ErrMalformed
}

// parseTag parses "host tag[pid]: msg".
func parseTag(s string, h *Header) error {
	sp := strings.IndexByte(s, ' ')
	if sp <= 0 {
		return ErrMalformed
	}
	h.Host = s[:sp]
	s = s[sp+1:]
	br := strings.IndexByte(s, '[')
	colon := strings.Index(s, ": ")
	if br < 0 || colon < 0 || br > colon {
		return ErrNotPostfix
	}
	tag := s[:br]
	end := strings.IndexByte(s[br:], ']')
	if end < 0 {
		return ErrMalformed
	}
	pid, err := strconv.Atoi(s[br+1 : br+end])
	if err != nil {
		return ErrMalformed
	}
	slash := strings.IndexByte(tag, '/')
	if slash <= 0 {
		return ErrNotPostfix
	}
	daemon := tag[strings.LastIndexByte(tag, '/')+1:]
	if !postfixDaemons[daemon] {
		return ErrNotPostfix
	}
	h.Inst = tag[:slash]
	h.Proc = daemon
	h.PID = pid
	if br+end+3 > len(s) {
		h.Msg = ""
	} else {
		h.Msg = s[br+end+3:]
	}
	return nil
}

// IsQueueID reports whether s looks like a Postfix queue ID (short hex or long format).
func IsQueueID(s string) bool {
	n := len(s)
	if n < 5 || n > 20 {
		return false
	}
	hex := true
	digit, upper := false, false
	for i := 0; i < n; i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'A' && c <= 'F':
			upper = true
		case c >= 'G' && c <= 'Z':
			upper = true
			hex = false
		case c >= 'a' && c <= 'z':
			hex = false
		default:
			return false
		}
	}
	if hex {
		return n <= 12 && s != "NOQUEUE"
	}
	// Long queue IDs: >= 12 chars of base-52ish alphabet, mixed case and digits.
	return n >= 12 && digit && upper
}

// Parse parses a full line into an Event.
func Parse(line string, clock *Clock) (Event, error) {
	h, err := ParseHeader(line, clock)
	if err != nil {
		return Event{}, err
	}
	ev := Event{TS: h.TS.Unix(), Host: h.Host, Inst: h.Inst, Proc: h.Proc, PID: h.PID, Raw: line, UID: -1}
	classify(&ev, h.Msg)
	return ev, nil
}

func classify(ev *Event, msg string) {
	if i := strings.Index(msg, ": "); i > 0 && IsQueueID(msg[:i]) {
		ev.QID = msg[:i]
		msg = msg[i+2:]
	} else if strings.HasPrefix(msg, "NOQUEUE: ") {
		msg = msg[len("NOQUEUE: "):]
	}
	switch ev.Proc {
	case "smtpd", "postscreen":
		parseSmtpd(ev, msg)
	case "cleanup":
		parseCleanup(ev, msg)
	case "pickup":
		parsePickup(ev, msg)
	case "qmgr", "oqmgr":
		parseQmgr(ev, msg)
	case "smtp", "lmtp", "pipe", "local", "virtual", "error", "retry", "discard":
		parseDelivery(ev, msg)
	case "bounce", "defer", "trace":
		parseBounce(ev, msg)
	case "master":
		switch {
		case strings.HasPrefix(msg, "daemon started"):
			ev.Kind = KindServiceStart
			ev.Text = msg
		case strings.HasPrefix(msg, "terminating on signal"):
			ev.Kind = KindServiceStop
			ev.Text = msg
		case strings.HasPrefix(msg, "warning:"):
			ev.Kind = KindWarning
		default:
			ev.Kind = KindNoise
		}
	case "anvil", "scache", "verify", "postfix-script", "tlsmgr", "trivial-rewrite", "proxymap":
		if strings.HasPrefix(msg, "warning:") || strings.HasPrefix(msg, "fatal:") || strings.HasPrefix(msg, "error:") {
			ev.Kind = KindWarning
		} else {
			ev.Kind = KindNoise
		}
	default:
		if strings.HasPrefix(msg, "warning:") {
			ev.Kind = KindWarning
		} else {
			ev.Kind = KindUnknown
		}
	}
}

// hostIP parses "host[ip]" (optionally followed by ":port") at the start of s
// and returns host, ip and the remainder after the closing bracket.
func hostIP(s string) (host, ip, rest string, ok bool) {
	b := strings.IndexByte(s, '[')
	if b < 0 {
		return "", "", s, false
	}
	e := strings.IndexByte(s[b:], ']')
	if e < 0 {
		return "", "", s, false
	}
	return s[:b], s[b+1 : b+e], s[b+e+1:], true
}

// angle extracts the value of key=<...> from s.
func angle(s, key string) (string, bool) {
	i := strings.Index(s, key+"=<")
	if i < 0 {
		return "", false
	}
	if i > 0 && s[i-1] != ' ' && s[i-1] != ',' && s[i-1] != ';' {
		// avoid matching "orig_to=<" when looking for "to=<"
		j := strings.Index(s[i+1:], " "+key+"=<")
		if j < 0 {
			return "", false
		}
		i = i + 1 + j + 1
	}
	v := s[i+len(key)+2:]
	e := strings.IndexByte(v, '>')
	if e < 0 {
		return "", false
	}
	return v[:e], true
}

func parseSmtpd(ev *Event, msg string) {
	switch {
	case strings.HasPrefix(msg, "connect from "):
		ev.Kind = KindConnect
		ev.Client, ev.IP, _, _ = hostIP(msg[len("connect from "):])
	case strings.HasPrefix(msg, "disconnect from "):
		ev.Kind = KindDisconnect
		var rest string
		ev.Client, ev.IP, rest, _ = hostIP(msg[len("disconnect from "):])
		ev.Text = strings.TrimSpace(rest)
		for _, f := range strings.Fields(rest) {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			okN, totN := counts(v)
			switch k {
			case "starttls":
				ev.TLS = okN
			case "auth":
				ev.AuthOK = okN
				ev.AuthKO = totN - okN
			}
		}
	case ev.QID != "" && strings.HasPrefix(msg, "client="):
		ev.Kind = KindClient
		parts := strings.Split(msg, ", ")
		ev.Client, ev.IP, _, _ = hostIP(strings.TrimPrefix(parts[0], "client="))
		for _, p := range parts[1:] {
			k, v, _ := strings.Cut(p, "=")
			switch k {
			case "sasl_method":
				ev.Method = v
			case "sasl_username":
				ev.User = v
			}
		}
	case strings.HasPrefix(msg, "reject: "), strings.HasPrefix(msg, "milter-reject: "):
		parseReject(ev, msg, KindReject)
	case strings.HasPrefix(msg, "discard: "), strings.HasPrefix(msg, "milter-discard: "):
		parseReject(ev, msg, KindDiscard)
	case strings.HasPrefix(msg, "hold: "), strings.HasPrefix(msg, "milter-hold: "):
		parseReject(ev, msg, KindHold)
	case strings.HasPrefix(msg, "lost connection after "), strings.HasPrefix(msg, "timeout after "),
		strings.HasPrefix(msg, "too many errors after "):
		ev.Kind = KindSessionAbort
		i := strings.Index(msg, " after ")
		rest := msg[i+len(" after "):]
		j := strings.Index(rest, " from ")
		if j >= 0 {
			ev.Stage = rest[:j]
			ev.Client, ev.IP, _, _ = hostIP(rest[j+len(" from "):])
		}
		ev.Text = msg[:i]
	case strings.HasPrefix(msg, "warning: ") && strings.Contains(msg, " authentication failed"):
		ev.Kind = KindSASLFail
		rest := msg[len("warning: "):]
		ev.Client, ev.IP, rest, _ = hostIP(rest)
		rest = strings.TrimPrefix(rest, ": ")
		// "SASL LOGIN authentication failed: reason"
		if strings.HasPrefix(rest, "SASL ") {
			f := strings.Fields(rest)
			if len(f) >= 2 {
				ev.Method = strings.ToUpper(f[1])
			}
		}
		if i := strings.Index(rest, "authentication failed"); i >= 0 {
			ev.Text = strings.TrimSpace(strings.TrimPrefix(rest[i+len("authentication failed"):], ":"))
		}
	case strings.HasPrefix(msg, "SSL_accept error from "):
		ev.Kind = KindTLSError
		ev.Client, ev.IP, _, _ = hostIP(msg[len("SSL_accept error from "):])
		ev.Text = "SSL_accept error"
	case strings.HasPrefix(msg, "warning: TLS library problem"):
		ev.Kind = KindTLSError
		ev.Text = "TLS library problem"
	case strings.Contains(msg, " TLS connection established from "):
		ev.Kind = KindTLS
		parseTLSEstablished(ev, msg, " TLS connection established from ")
	case strings.HasPrefix(msg, "discarding EHLO keywords"):
		ev.Kind = KindNoise
	case strings.HasPrefix(msg, "warning: "):
		ev.Kind = KindWarning
	default:
		ev.Kind = KindUnknown
	}
}

// KindTLS is a successful TLS handshake (only logged with tls_loglevel >= 1).
const KindTLS Kind = "tls"

func parseTLSEstablished(ev *Event, msg, sep string) {
	i := strings.Index(msg, sep)
	ev.Status = msg[:i] // Anonymous / Trusted / Untrusted / Verified
	rest := msg[i+len(sep):]
	var r string
	ev.Client, ev.IP, r, _ = hostIP(rest)
	r = strings.TrimPrefix(r, ":")
	if c := strings.Index(r, ": "); c >= 0 {
		r = r[c+2:]
	}
	f := strings.Fields(r)
	if len(f) > 0 {
		ev.Text = f[0] // protocol, e.g. TLSv1.3
	}
	if j := strings.Index(r, "with cipher "); j >= 0 {
		g := strings.Fields(r[j+len("with cipher "):])
		if len(g) > 0 {
			ev.Relay = g[0] // cipher (stored in relay column)
		}
	}
}

// counts parses "N" or "N/M" (successful/total).
func counts(v string) (ok, total int) {
	a, b, slash := strings.Cut(v, "/")
	ok, _ = strconv.Atoi(a)
	if slash {
		total, _ = strconv.Atoi(b)
	} else {
		total = ok
	}
	return ok, total
}

// parseReject handles smtpd and cleanup reject/discard/hold lines:
//
//	reject: RCPT from host[ip]: 554 5.7.1 <x@y>: Relay access denied; from=<a> to=<b> proto=ESMTP helo=<h>
//	milter-reject: END-OF-MESSAGE from host[ip]: 4.7.1 Try again later; from=<a> to=<b> proto=ESMTP helo=<h>
//	reject: header Subject: xxx from host[ip]; from=<a> to=<b> proto=ESMTP helo=<h>: 5.7.1 text
func parseReject(ev *Event, msg string, kind Kind) {
	ev.Kind = kind
	action, rest, _ := strings.Cut(msg, ": ")
	ev.Status = action
	if sp := strings.IndexByte(rest, ' '); sp > 0 {
		ev.Stage = rest[:sp]
	}
	if ev.Stage == "header" || ev.Stage == "body" {
		// header/body checks: the reason follows the envelope list.
		semi := strings.LastIndex(rest, "; from=")
		if semi >= 0 {
			if k := strings.LastIndex(rest[:semi], " from "); k >= 0 {
				ev.Client, ev.IP, _, _ = hostIP(rest[k+len(" from ") : semi])
			}
			env := rest[semi+2:]
			ev.Sender, _ = angle(env, "from")
			ev.Orig, _ = angle(env, "to")
			if c := strings.Index(env, ">: "); c >= 0 {
				parseReplyText(ev, env[c+3:])
			} else if c := strings.LastIndex(env, ": "); c >= 0 {
				parseReplyText(ev, env[c+2:])
			}
		}
		return
	}
	if k := strings.Index(rest, " from "); k >= 0 {
		var after string
		ev.Client, ev.IP, after, _ = hostIP(rest[k+len(" from "):])
		after = strings.TrimPrefix(after, ": ")
		reply := after
		if semi := strings.Index(after, "; from="); semi >= 0 {
			reply = after[:semi]
			env := after[semi+2:]
			ev.Sender, _ = angle(env, "from")
			ev.Orig, _ = angle(env, "to")
		}
		parseReplyText(ev, reply)
	}
}

// parseReplyText parses "[CODE ][DSN ]text" into Code, DSN and Text.
func parseReplyText(ev *Event, s string) {
	s = strings.TrimSpace(s)
	if len(s) >= 4 && s[3] == ' ' && isDigits(s[:3]) {
		ev.Code, _ = strconv.Atoi(s[:3])
		s = s[4:]
	}
	if sp := strings.IndexByte(s, ' '); sp > 0 && isDSN(s[:sp]) {
		ev.DSN = s[:sp]
		s = s[sp+1:]
	} else if isDSN(s) {
		ev.DSN = s
		s = ""
	}
	if ev.Code == 0 && ev.DSN != "" {
		ev.Code = int(ev.DSN[0]-'0') * 100
	}
	ev.Text = s
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func isDSN(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || len(parts[0]) != 1 {
		return false
	}
	for _, p := range parts {
		if !isDigits(p) || len(p) > 3 {
			return false
		}
	}
	return parts[0] == "2" || parts[0] == "4" || parts[0] == "5"
}

func parseCleanup(ev *Event, msg string) {
	switch {
	case strings.HasPrefix(msg, "message-id="):
		ev.Kind = KindMessageID
		v := msg[len("message-id="):]
		v = strings.TrimSuffix(strings.TrimPrefix(v, "<"), ">")
		ev.Text = v
	case strings.HasPrefix(msg, "resent-message-id="):
		ev.Kind = KindNoise
	case strings.HasPrefix(msg, "reject: "), strings.HasPrefix(msg, "milter-reject: "):
		parseReject(ev, msg, KindReject)
	case strings.HasPrefix(msg, "discard: "), strings.HasPrefix(msg, "milter-discard: "):
		parseReject(ev, msg, KindDiscard)
	case strings.HasPrefix(msg, "hold: "), strings.HasPrefix(msg, "milter-hold: "):
		parseReject(ev, msg, KindHold)
	case strings.HasPrefix(msg, "warning: header "), strings.HasPrefix(msg, "warning: body "):
		ev.Kind = KindNoise // header_checks WARN (logs the subject: never stored)
	case strings.HasPrefix(msg, "warning: "):
		ev.Kind = KindWarning
	case strings.HasPrefix(msg, "redirect: "), strings.HasPrefix(msg, "replace: "),
		strings.HasPrefix(msg, "info: "), strings.HasPrefix(msg, "filter: "),
		strings.HasPrefix(msg, "prepend: "):
		ev.Kind = KindNoise
	default:
		ev.Kind = KindUnknown
	}
}

func parsePickup(ev *Event, msg string) {
	if !strings.HasPrefix(msg, "uid=") || ev.QID == "" {
		if strings.HasPrefix(msg, "warning: ") {
			ev.Kind = KindWarning
		} else {
			ev.Kind = KindUnknown
		}
		return
	}
	ev.Kind = KindPickup
	f := strings.Fields(msg)
	ev.UID, _ = strconv.Atoi(strings.TrimPrefix(f[0], "uid="))
	ev.Sender, _ = angle(msg, "from")
}

func parseQmgr(ev *Event, msg string) {
	switch {
	case ev.QID == "":
		if strings.HasPrefix(msg, "warning: ") {
			ev.Kind = KindWarning
		} else {
			ev.Kind = KindNoise
		}
	case msg == "removed":
		ev.Kind = KindRemoved
	case strings.HasPrefix(msg, "from=") && strings.Contains(msg, "status=expired"):
		ev.Kind = KindExpired
		ev.Sender, _ = angle(msg, "from")
	case strings.HasPrefix(msg, "from=") && strings.Contains(msg, "nrcpt="):
		ev.Kind = KindActive
		ev.Sender, _ = angle(msg, "from")
		for _, p := range strings.Split(msg, ", ") {
			k, v, _ := strings.Cut(p, "=")
			switch k {
			case "size":
				ev.Size, _ = strconv.ParseInt(v, 10, 64)
			case "nrcpt":
				n, _ := strings.CutSuffix(v, " (queue active)")
				if sp := strings.IndexByte(n, ' '); sp > 0 {
					n = n[:sp]
				}
				ev.Nrcpt, _ = strconv.Atoi(n)
			}
		}
	case strings.HasPrefix(msg, "skipped, still being delivered"):
		ev.Kind = KindNoise
	case strings.HasPrefix(msg, "warning: "):
		ev.Kind = KindWarning
	default:
		ev.Kind = KindUnknown
	}
}

func parseDelivery(ev *Event, msg string) {
	switch {
	case ev.QID != "" && strings.HasPrefix(msg, "to=<"):
		ev.Kind = KindDelivery
		head, tail, found := strings.Cut(msg, "status=")
		if !found {
			ev.Kind = KindUnknown
			return
		}
		ev.Rcpt, _ = angle(head, "to")
		ev.Orig, _ = angle(head, "orig_to")
		for _, p := range strings.Split(strings.TrimSuffix(strings.TrimSpace(head), ","), ", ") {
			k, v, _ := strings.Cut(p, "=")
			switch k {
			case "relay":
				ev.Relay = v
			case "delay":
				ev.Delay, _ = strconv.ParseFloat(v, 64)
			case "delays":
				ev.Delays = v
			case "dsn":
				ev.DSN = v
			}
		}
		st, text, _ := strings.Cut(tail, " ")
		ev.Status = strings.TrimSuffix(st, ",")
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") {
			text = text[1 : len(text)-1]
		}
		ev.Text = text
	case ev.QID != "" && strings.HasPrefix(msg, "host ") &&
		(strings.Contains(msg, " said: ") || strings.Contains(msg, " refused to talk to me: ")):
		ev.Kind = KindHostReply
		ev.Client, ev.IP, _, _ = hostIP(msg[len("host "):])
		ev.Text = msg
	case ev.QID != "" && strings.HasPrefix(msg, "lost connection with "),
		ev.QID != "" && strings.HasPrefix(msg, "conversation with "):
		ev.Kind = KindHostReply
		ev.Text = msg
	case strings.Contains(msg, " TLS connection established to "):
		ev.Kind = KindTLS
		parseTLSEstablished(ev, msg, " TLS connection established to ")
	case strings.HasPrefix(msg, "connect to "), strings.HasPrefix(msg, "enabling PIX workarounds"),
		strings.HasPrefix(msg, "SSL_connect error"):
		ev.Kind = KindNoise
	case strings.HasPrefix(msg, "warning: "):
		ev.Kind = KindWarning
	default:
		ev.Kind = KindUnknown
	}
}

func parseBounce(ev *Event, msg string) {
	if ev.QID != "" && strings.Contains(msg, " notification: ") {
		i := strings.LastIndex(msg, " notification: ")
		nq := strings.TrimSpace(msg[i+len(" notification: "):])
		if IsQueueID(nq) {
			ev.Kind = KindNotification
			ev.Status = msg[:i] // "sender non-delivery", "sender delay", ...
			ev.NewQID = nq
			return
		}
	}
	if strings.HasPrefix(msg, "warning: ") {
		ev.Kind = KindWarning
		return
	}
	ev.Kind = KindUnknown
}

// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import (
	"testing"
	"time"
)

// All fixtures below are synthetic: they reproduce the formats observed in
// the reference log (see LOG_ANALYSIS.md) with documentation-only domains
// and addresses (example.org/.net/.com, RFC 5737 IP blocks).

func rome(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func newClock(t *testing.T) *Clock {
	ref := time.Date(2026, 10, 13, 12, 0, 0, 0, rome(t))
	return NewClock(rome(t), 0, ref, nil)
}

func mustParse(t *testing.T, c *Clock, line string) Event {
	t.Helper()
	ev, err := Parse(line, c)
	if err != nil {
		t.Fatalf("parse %q: %v", line, err)
	}
	return ev
}

func TestParseHeaderBSD(t *testing.T) {
	c := newClock(t)
	ev := mustParse(t, c, "Oct 11 03:25:42 mx1 postfix/smtpd[1076730]: DA7E57F733: client=unknown[192.0.2.14]")
	want := time.Date(2026, 10, 11, 1, 25, 42, 0, time.UTC).Unix()
	if ev.TS != want {
		t.Errorf("ts = %v, want %v", time.Unix(ev.TS, 0).UTC(), time.Unix(want, 0).UTC())
	}
	if ev.Host != "mx1" || ev.Inst != "postfix" || ev.Proc != "smtpd" || ev.PID != 1076730 || ev.QID != "DA7E57F733" {
		t.Errorf("header fields: %+v", ev)
	}
	if ev.Kind != KindClient || ev.Client != "unknown" || ev.IP != "192.0.2.14" {
		t.Errorf("client: %+v", ev)
	}
}

func TestParseHeaderSingleDigitDayAndService(t *testing.T) {
	c := newClock(t)
	ev := mustParse(t, c, "Oct  2 10:00:00 mx1 postfix-out/submission/smtpd[42]: connect from a.example.org[192.0.2.1]")
	if ev.Inst != "postfix-out" || ev.Proc != "smtpd" || ev.Kind != KindConnect {
		t.Fatalf("got %+v", ev)
	}
	if got := time.Unix(ev.TS, 0).In(rome(t)).Day(); got != 2 {
		t.Errorf("day = %d", got)
	}
}

func TestParseHeaderRFC3339(t *testing.T) {
	c := newClock(t)
	ev := mustParse(t, c, "2026-10-11T03:25:42.123456+02:00 mx1 postfix/qmgr[5]: DA7E57F733: removed")
	if ev.TS != time.Date(2026, 10, 11, 1, 25, 42, 0, time.UTC).Unix() || ev.Kind != KindRemoved {
		t.Fatalf("got %+v", ev)
	}
}

func TestNotPostfix(t *testing.T) {
	c := newClock(t)
	for _, l := range []string{
		"Oct 11 03:25:42 mx1 dovecot[12]: imap-login: Login: user=<x>",
		"Oct 11 03:25:42 mx1 opendkim[12]: ABC: DKIM-Signature field added",
		"Oct 11 03:25:42 mx1 kernel: something",
	} {
		if _, err := Parse(l, c); err != ErrNotPostfix {
			t.Errorf("%q: err=%v, want ErrNotPostfix", l, err)
		}
	}
	if _, err := Parse("garbage", c); err != ErrMalformed {
		t.Errorf("garbage: %v", err)
	}
}

func TestYearInferenceAndRollover(t *testing.T) {
	loc := rome(t)
	// File modified on 2 Jan 2027, starting in December 2026.
	c := NewClock(loc, 0, time.Date(2027, 1, 2, 0, 0, 0, 0, loc), nil)
	e1 := mustParse(t, c, "Dec 31 23:59:59 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed")
	e2 := mustParse(t, c, "Jan  1 00:00:01 mx1 postfix/qmgr[1]: BBBBBBBBBB: removed")
	if y := time.Unix(e1.TS, 0).In(loc).Year(); y != 2026 {
		t.Errorf("e1 year %d", y)
	}
	if y := time.Unix(e2.TS, 0).In(loc).Year(); y != 2027 {
		t.Errorf("e2 year %d", y)
	}
	// Reference in October: a line from November must be the previous year.
	c2 := NewClock(loc, 0, time.Date(2026, 10, 13, 0, 0, 0, 0, loc), nil)
	e3 := mustParse(t, c2, "Nov 20 10:00:00 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed")
	if y := time.Unix(e3.TS, 0).In(loc).Year(); y != 2025 {
		t.Errorf("e3 year %d", y)
	}
	// Month boundary: mtime 30 Sep 23:59:50, first line 1 Oct (clock skew).
	c5 := NewClock(loc, 0, time.Date(2026, 9, 30, 23, 59, 50, 0, loc), nil)
	e6 := mustParse(t, c5, "Oct  1 00:00:05 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed")
	if y := time.Unix(e6.TS, 0).In(loc).Year(); y != 2026 {
		t.Errorf("month boundary year %d", y)
	}
	// Fixed year.
	c3 := NewClock(loc, 2024, time.Time{}, nil)
	e4 := mustParse(t, c3, "Feb 29 10:00:00 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed")
	if y := time.Unix(e4.TS, 0).In(loc).Year(); y != 2024 {
		t.Errorf("e4 year %d", y)
	}
	// Restored state keeps the year.
	st := c.State()
	c4 := NewClock(loc, 0, time.Date(2030, 1, 1, 0, 0, 0, 0, loc), &st)
	e5 := mustParse(t, c4, "Jan  1 00:00:02 mx1 postfix/qmgr[1]: BBBBBBBBBB: removed")
	if y := time.Unix(e5.TS, 0).In(loc).Year(); y != 2027 {
		t.Errorf("e5 year %d", y)
	}
}

func TestDSTFallBack(t *testing.T) {
	loc := rome(t)
	// 25 Oct 2026: clocks go back from 03:00 CEST to 02:00 CET.
	c := NewClock(loc, 0, time.Date(2026, 10, 26, 0, 0, 0, 0, loc), nil)
	lines := []string{
		"Oct 25 02:30:00 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed", // CEST (00:30 UTC)
		"Oct 25 02:59:59 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed", // CEST (00:59:59 UTC)
		"Oct 25 02:00:00 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed", // CET  (01:00 UTC)
		"Oct 25 02:30:00 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed", // CET  (01:30 UTC)
		"Oct 25 03:00:00 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed", // CET  (02:00 UTC)
	}
	want := []string{"00:30:00", "00:59:59", "01:00:00", "01:30:00", "02:00:00"}
	for i, l := range lines {
		ev := mustParse(t, c, l)
		got := time.Unix(ev.TS, 0).UTC().Format("15:04:05")
		if got != want[i] {
			t.Errorf("line %d: %s UTC, want %s", i, got, want[i])
		}
	}
}

func TestDSTSpringForward(t *testing.T) {
	loc := rome(t)
	c := NewClock(loc, 0, time.Date(2026, 3, 30, 0, 0, 0, 0, loc), nil)
	a := mustParse(t, c, "Mar 29 01:59:59 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed")
	b := mustParse(t, c, "Mar 29 03:00:00 mx1 postfix/qmgr[1]: AAAAAAAAAA: removed")
	if b.TS-a.TS != 1 {
		t.Errorf("spring forward gap: %d s", b.TS-a.TS)
	}
}

func TestQueueID(t *testing.T) {
	for s, want := range map[string]bool{
		"DA7E57F733": true, "7DB3E7F733": true, "ABCDEF": true,
		"4bKq2c0Lr5z9tJP": true, // long format
		"NOQUEUE":         false, "statistics": false, "warning": false, "client=x": false,
	} {
		if IsQueueID(s) != want {
			t.Errorf("IsQueueID(%q) = %v", s, !want)
		}
	}
}

func TestSmtpdEvents(t *testing.T) {
	c := newClock(t)
	cases := []struct {
		line  string
		check func(Event) bool
	}{
		{"Oct 11 03:25:42 mx1 postfix/smtpd[1]: connect from unknown[192.0.2.14]",
			func(e Event) bool { return e.Kind == KindConnect && e.IP == "192.0.2.14" && e.Client == "unknown" }},
		{"Oct 11 03:25:43 mx1 postfix/smtpd[1]: disconnect from unknown[192.0.2.14] ehlo=2 starttls=1 auth=0/1 mail=1 rcpt=1 data=1 quit=1 commands=7/8",
			func(e Event) bool { return e.Kind == KindDisconnect && e.TLS == 1 && e.AuthOK == 0 && e.AuthKO == 1 }},
		{"Oct 11 08:00:01 mx1 postfix/smtpd[1]: 0C1AF7F739: client=unknown[198.51.100.11], sasl_method=LOGIN, sasl_username=app1",
			func(e Event) bool { return e.Kind == KindClient && e.Method == "LOGIN" && e.User == "app1" }},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: warning: unknown[203.0.113.83]: SASL LOGIN authentication failed: UGFzc3dvcmQ6",
			func(e Event) bool {
				return e.Kind == KindSASLFail && e.IP == "203.0.113.83" && e.Method == "LOGIN" && e.Text == "UGFzc3dvcmQ6"
			}},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: warning: unknown[203.0.113.83]: SASL PLAIN authentication failed: ",
			func(e Event) bool { return e.Kind == KindSASLFail && e.Method == "PLAIN" && e.Text == "" }},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: warning: unknown[203.0.113.83]: SASL login authentication failed: Connection lost to authentication server",
			func(e Event) bool {
				return e.Kind == KindSASLFail && e.Method == "LOGIN" && e.Text == "Connection lost to authentication server"
			}},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: NOQUEUE: reject: RCPT from unknown[203.0.113.9]: 554 5.7.1 <x@example.com>: Relay access denied; from=<a@example.net> to=<x@example.com> proto=ESMTP helo=<h.example.net>",
			func(e Event) bool {
				return e.Kind == KindReject && e.QID == "" && e.Stage == "RCPT" && e.Code == 554 && e.DSN == "5.7.1" &&
					e.Text == "<x@example.com>: Relay access denied" && e.Sender == "a@example.net" && e.Orig == "x@example.com" && e.IP == "203.0.113.9"
			}},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: ABCDEF1234: reject: RCPT from unknown[203.0.113.9]: 450 4.7.1 Client host rejected: cannot find your hostname; from=<a@example.net> to=<x@example.com> proto=ESMTP helo=<h>",
			func(e Event) bool { return e.Kind == KindReject && e.QID == "ABCDEF1234" && e.Code == 450 }},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: lost connection after AUTH from unknown[203.0.113.9]",
			func(e Event) bool { return e.Kind == KindSessionAbort && e.Stage == "AUTH" && e.IP == "203.0.113.9" }},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: timeout after END-OF-MESSAGE from unknown[203.0.113.9]",
			func(e Event) bool { return e.Kind == KindSessionAbort && e.Stage == "END-OF-MESSAGE" }},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: SSL_accept error from unknown[203.0.113.9]: -1",
			func(e Event) bool { return e.Kind == KindTLSError && e.IP == "203.0.113.9" }},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: warning: TLS library problem: error:0A000076:SSL routines::no suitable signature algorithm",
			func(e Event) bool { return e.Kind == KindTLSError }},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: Anonymous TLS connection established from a.example.net[198.51.100.5]: TLSv1.3 with cipher TLS_AES_256_GCM_SHA384 (256/256 bits)",
			func(e Event) bool {
				return e.Kind == KindTLS && e.Status == "Anonymous" && e.Text == "TLSv1.3" && e.IP == "198.51.100.5"
			}},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: discarding EHLO keywords: CHUNKING",
			func(e Event) bool { return e.Kind == KindNoise }},
		{"Oct 11 08:00:03 mx1 postfix/smtpd[1]: warning: hostname x.example.net does not resolve to address 192.0.2.1",
			func(e Event) bool { return e.Kind == KindWarning }},
	}
	for _, tc := range cases {
		e := mustParse(t, c, tc.line)
		if !tc.check(e) {
			t.Errorf("%s\n  => %+v", tc.line, e)
		}
	}
}

func TestCleanupEvents(t *testing.T) {
	c := newClock(t)
	cases := []struct {
		line  string
		check func(Event) bool
	}{
		{"Oct 11 03:25:42 mx1 postfix/cleanup[2]: DA7E57F733: message-id=<20261011012542.A900C580828@h.example.org>",
			func(e Event) bool {
				return e.Kind == KindMessageID && e.Text == "20261011012542.A900C580828@h.example.org"
			}},
		{"Oct 11 03:25:42 mx1 postfix/cleanup[2]: DA7E57F733: message-id=<>",
			func(e Event) bool { return e.Kind == KindMessageID && e.Text == "" }},
		{"Oct 11 03:25:42 mx1 postfix/cleanup[2]: DA7E57F733: warning: header Subject: hello from unknown[192.0.2.1]; from=<a@example.org> to=<b@example.org> proto=ESMTP helo=<h>",
			func(e Event) bool { return e.Kind == KindNoise }},
		{"Oct 11 03:25:42 mx1 postfix/cleanup[2]: DA7E57F733: milter-reject: END-OF-MESSAGE from mail-x.example.com[198.51.100.5]: 4.7.1 Try again later; from=<a@example.com> to=<b@example.org> proto=ESMTP helo=<h.example.com>",
			func(e Event) bool {
				return e.Kind == KindReject && e.Status == "milter-reject" && e.Stage == "END-OF-MESSAGE" && e.Code == 400 &&
					e.DSN == "4.7.1" && e.Text == "Try again later" && e.IP == "198.51.100.5" && e.Sender == "a@example.com"
			}},
		{"Oct 11 03:25:42 mx1 postfix/cleanup[2]: DA7E57F733: milter-reject: END-OF-MESSAGE from mail-x.example.com[198.51.100.5]: 5.7.1 Spam message rejected; from=<a@example.com> to=<b@example.org> proto=ESMTP helo=<h>",
			func(e Event) bool { return e.Kind == KindReject && e.Code == 500 && e.Text == "Spam message rejected" }},
		{"Oct 11 03:25:42 mx1 postfix/cleanup[2]: DA7E57F733: reject: header Subject: buy now from a.example.com[198.51.100.5]; from=<a@example.com> to=<b@example.org> proto=ESMTP helo=<h>: 5.7.1 message content rejected",
			func(e Event) bool {
				return e.Kind == KindReject && e.Stage == "header" && e.DSN == "5.7.1" && e.Code == 500 &&
					e.Text == "message content rejected" && e.IP == "198.51.100.5" && e.Orig == "b@example.org"
			}},
		{"Oct 11 03:25:42 mx1 postfix/cleanup[2]: DA7E57F733: discard: header Subject: junk from a.example.com[198.51.100.5]; from=<a@example.com> to=<b@example.org> proto=ESMTP helo=<h>: dropped",
			func(e Event) bool { return e.Kind == KindDiscard && e.Stage == "header" && e.Sender == "a@example.com" }},
		{"Oct 11 03:25:42 mx1 postfix/cleanup[2]: DA7E57F733: hold: header X-Spam: yes from a.example.com[198.51.100.5]; from=<a@example.com> to=<b@example.org> proto=ESMTP helo=<h>",
			func(e Event) bool { return e.Kind == KindHold }},
	}
	for _, tc := range cases {
		e := mustParse(t, c, tc.line)
		if !tc.check(e) {
			t.Errorf("%s\n  => %+v", tc.line, e)
		}
	}
}

func TestQueueEvents(t *testing.T) {
	c := newClock(t)
	cases := []struct {
		line  string
		check func(Event) bool
	}{
		{"Oct 11 03:22:02 mx1 postfix/pickup[3]: 7DB3E7F733: uid=0 from=<root>",
			func(e Event) bool { return e.Kind == KindPickup && e.UID == 0 && e.Sender == "root" }},
		{"Oct 11 03:25:43 mx1 postfix/pickup[3]: 206487F6D9: uid=97 from=<monitor@example.org>",
			func(e Event) bool { return e.Kind == KindPickup && e.UID == 97 && e.Sender == "monitor@example.org" }},
		{"Oct 11 03:25:43 mx1 postfix/qmgr[4]: DA7E57F733: from=<monitor@example.org>, size=8585, nrcpt=1 (queue active)",
			func(e Event) bool {
				return e.Kind == KindActive && e.Sender == "monitor@example.org" && e.Size == 8585 && e.Nrcpt == 1
			}},
		{"Oct 11 03:25:43 mx1 postfix/qmgr[4]: DA7E57F733: from=<>, size=3263, nrcpt=22 (queue active)",
			func(e Event) bool { return e.Kind == KindActive && e.Sender == "" && e.Nrcpt == 22 }},
		{"Oct 11 03:25:43 mx1 postfix/qmgr[4]: DA7E57F733: removed",
			func(e Event) bool { return e.Kind == KindRemoved }},
		{"Oct 11 22:20:09 mx1 postfix/qmgr[4]: 97DAF7F72F: from=<app@example.net>, status=expired, returned to sender",
			func(e Event) bool { return e.Kind == KindExpired && e.Sender == "app@example.net" }},
		{"Oct 11 22:20:09 mx1 postfix/bounce[5]: 97DAF7F72F: sender non-delivery notification: 6435C7F726",
			func(e Event) bool {
				return e.Kind == KindNotification && e.Status == "sender non-delivery" && e.NewQID == "6435C7F726"
			}},
		{"Oct 11 06:33:16 mx1 postfix/bounce[5]: 5702F7F6D9: sender delivery status notification: EB7CA7F641",
			func(e Event) bool { return e.Kind == KindNotification && e.Status == "sender delivery status" }},
		{"Oct 12 00:00:02 mx1 postfix/master[6]: daemon started -- version 3.5.8, configuration /etc/postfix",
			func(e Event) bool { return e.Kind == KindServiceStart }},
		{"Oct 12 00:00:01 mx1 postfix/master[6]: terminating on signal 15",
			func(e Event) bool { return e.Kind == KindServiceStop }},
		{"Oct 12 00:00:01 mx1 postfix/anvil[7]: statistics: max connection rate 4/60s for (smtp:192.0.2.1) at Oct 12 00:00:00",
			func(e Event) bool { return e.Kind == KindNoise }},
	}
	for _, tc := range cases {
		e := mustParse(t, c, tc.line)
		if !tc.check(e) {
			t.Errorf("%s\n  => %+v", tc.line, e)
		}
	}
}

func TestDeliveryEvents(t *testing.T) {
	c := newClock(t)
	cases := []struct {
		line  string
		check func(Event) bool
	}{
		{"Oct 11 03:25:43 mx1 postfix/pipe[9]: DA7E57F733: to=<ops@example.org>, relay=filter, delay=0.26, delays=0.21/0.02/0/0.03, dsn=2.0.0, status=sent (delivered via filter service)",
			func(e Event) bool {
				return e.Kind == KindDelivery && e.Proc == "pipe" && e.Relay == "filter" && e.Delay == 0.26 &&
					e.Delays == "0.21/0.02/0/0.03" && e.DSN == "2.0.0" && e.Status == "sent" && e.Text == "delivered via filter service"
			}},
		{"Oct 11 03:25:44 mx1 postfix/smtp[9]: 206487F6D9: to=<ops@example.org>, relay=mx.example.net[198.51.100.27]:25, delay=1.3, delays=0.01/0/0.35/0.93, dsn=2.6.0, status=sent (250 2.6.0 <id@example.org> [InternalId=1, Hostname=x] 9000 bytes in 0.1, 80 KB/sec Queued mail for delivery)",
			func(e Event) bool {
				return e.Relay == "mx.example.net[198.51.100.27]:25" && e.Status == "sent" && e.Rcpt == "ops@example.org"
			}},
		{"Oct 11 03:22:03 mx1 postfix/smtp[9]: 7DB3E7F733: to=<root@example.org>, orig_to=<root>, relay=mx.example.net[198.51.100.27]:25, delay=1, delays=0.01/0/0.35/0.6, dsn=2.6.0, status=sent (250 ok)",
			func(e Event) bool { return e.Rcpt == "root@example.org" && e.Orig == "root" && e.Delay == 1 }},
		{"Oct 11 04:20:35 mx1 postfix/smtp[9]: 93BE27F6E3: to=<u@h.example.org>, relay=none, delay=424528, delays=424498/0.08/30/0, dsn=4.4.1, status=deferred (connect to h.example.org[192.0.2.78]:25: Connection timed out)",
			func(e Event) bool {
				return e.Status == "deferred" && e.Relay == "none" && e.DSN == "4.4.1" &&
					e.Text == "connect to h.example.org[192.0.2.78]:25: Connection timed out"
			}},
		{"Oct 11 06:40:05 mx1 postfix/error[9]: 93BE27F6E3: to=<u@h.example.org>, relay=none, delay=432898, delays=432898/0.01/0/0.04, dsn=4.4.1, status=deferred (delivery temporarily suspended: connect to h.example.org[192.0.2.78]:25: Connection timed out)",
			func(e Event) bool { return e.Proc == "error" && e.Status == "deferred" }},
		{"Oct 11 06:40:05 mx1 postfix/smtp[9]: AB12CD34EF: to=<nobody@example.com>, relay=mx.example.com[203.0.113.5]:25, delay=1.2, delays=0.1/0/0.5/0.6, dsn=5.1.1, status=bounced (host mx.example.com[203.0.113.5] said: 550 5.1.1 <nobody@example.com>: Recipient address rejected (in reply to RCPT TO command))",
			func(e Event) bool { return e.Status == "bounced" && e.DSN == "5.1.1" }},
		{"Oct 11 23:00:29 mx1 postfix/lmtp[9]: 485DE7F726: to=<box@example.org>, relay=mx1.example.org[private/dovecot-lmtp], delay=0.05, delays=0.02/0.02/0.01/0.01, dsn=2.1.5, status=deliverable (250 2.1.5 OK)",
			func(e Event) bool {
				return e.Status == "deliverable" && e.Relay == "mx1.example.org[private/dovecot-lmtp]"
			}},
		{"Oct 11 03:40:06 mx1 postfix/smtp[9]: 97DAF7F72F: host mx.example.com[203.0.113.145] said: 450 4.1.2 <u@example.com>: Recipient address rejected: Sender domain not found. (in reply to RCPT TO command)",
			func(e Event) bool { return e.Kind == KindHostReply && e.IP == "203.0.113.145" }},
		{"Oct 11 03:40:06 mx1 postfix/smtp[9]: connect to h.example.org[192.0.2.78]:25: Connection timed out",
			func(e Event) bool { return e.Kind == KindNoise && e.QID == "" }},
		{"Oct 11 03:40:06 mx1 postfix/discard[9]: AB12CD34EF: to=<x@example.org>, relay=none, delay=0.1, delays=0.1/0/0/0, dsn=2.0.0, status=sent (discarded)",
			func(e Event) bool { return e.Kind == KindDelivery && e.Proc == "discard" && e.Status == "sent" }},
		{"Oct 11 03:40:06 mx1 postfix/smtp[9]: Verified TLS connection established to mx.example.com[203.0.113.5]:25: TLSv1.3 with cipher TLS_AES_256_GCM_SHA384 (256/256 bits)",
			func(e Event) bool {
				return e.Kind == KindTLS && e.Status == "Verified" && e.Text == "TLSv1.3" && e.Relay == "TLS_AES_256_GCM_SHA384"
			}},
	}
	for _, tc := range cases {
		e := mustParse(t, c, tc.line)
		if !tc.check(e) {
			t.Errorf("%s\n  => %+v", tc.line, e)
		}
	}
}

// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package logparse

import (
	"testing"
	"time"
)

var benchLines = []string{
	"Oct 11 03:25:42 mx1 postfix/smtpd[1076730]: DA7E57F733: client=unknown[192.0.2.14]",
	"Oct 11 03:25:42 mx1 postfix/cleanup[1077089]: DA7E57F733: message-id=<20261011012542.A900C580828@h.example.org>",
	"Oct 11 03:25:43 mx1 postfix/qmgr[1035434]: DA7E57F733: from=<monitor@example.org>, size=8585, nrcpt=1 (queue active)",
	"Oct 11 03:25:44 mx1 postfix/smtp[1077029]: 206487F6D9: to=<ops@example.org>, relay=mx.example.net[198.51.100.27]:25, delay=1.3, delays=0.01/0/0.35/0.93, dsn=2.6.0, status=sent (250 2.6.0 <id@example.org> Queued mail for delivery)",
	"Oct 11 03:25:44 mx1 postfix/smtpd[1076730]: disconnect from unknown[192.0.2.14] ehlo=2 starttls=1 mail=1 rcpt=1 data=1 quit=1 commands=7",
	"Oct 11 03:25:44 mx1 postfix/smtpd[1076730]: discarding EHLO keywords: CHUNKING",
	"Oct 11 03:25:44 mx1 postfix/cleanup[1077089]: DA7E57F733: milter-reject: END-OF-MESSAGE from mail-x.example.com[198.51.100.5]: 4.7.1 Try again later; from=<a@example.com> to=<b@example.org> proto=ESMTP helo=<h>",
}

// BenchmarkParse measures parsing of a representative mix of lines.
func BenchmarkParse(b *testing.B) {
	loc, _ := time.LoadLocation("Europe/Rome")
	c := NewClock(loc, 2026, time.Time{}, nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(benchLines[i%len(benchLines)], c); err != nil {
			b.Fatal(err)
		}
	}
}

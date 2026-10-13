// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package norm

import (
	"strings"
	"testing"
)

func TestDomain(t *testing.T) {
	for in, want := range map[string]string{
		"a@Example.ORG": "example.org", "": NullSender, "root": "(local)", "x@": "(local)",
	} {
		if got := Domain(in); got != want {
			t.Errorf("Domain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReason(t *testing.T) {
	cases := map[string]string{
		"<x@example.com>: Relay access denied":                          "<addr>: Relay access denied",
		"connect to h.example.org[192.0.2.78]:25: Connection timed out": "connect to h.example.org: Connection timed out",
		"host mx.example.com[203.0.113.5] said: 550 5.1.1 <nobody@example.com>: Recipient address rejected: User unknown (in reply to RCPT TO command)": "550 5.1.1 <addr>: Recipient address rejected: User unknown",
		"delivery temporarily suspended: connect to h.example.org[192.0.2.78]:25: Connection timed out":                                                 "delivery temporarily suspended: connect to h.example.org: Connection timed out",
		"Try again later": "Try again later",
		"":                "(no text)",
		"250 2.6.0 <20261011012542.A900C580828@h.example.org> [InternalId=8078833487551] Queued": "250 2.6.0 <addr> [InternalId=#] Queued",
	}
	for in, want := range cases {
		if got := Reason(in); got != want {
			t.Errorf("Reason(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	long := Reason(strings.Repeat("è", 200))
	if !strings.HasSuffix(long, "…") || len(long) > maxReason+len("…") {
		t.Errorf("truncation: %d bytes", len(long))
	}
	multi := Reason("host mx[192.0.2.1] said: 550-5.1.1 The email account that you tried to reach does not exist. 550-5.1.1 Please try 550 5.1.1 https://support.example.com/x - gsmtp (in reply to RCPT TO command)")
	if strings.Count(multi, "5.1.1") != 1 || strings.Contains(multi, "https") {
		t.Errorf("multi-line: %q", multi)
	}
}

func TestAnonymizer(t *testing.T) {
	a := NewAnonymizer([]byte("k"), true, true)
	e1, e2 := a.Email("Alice@example.org"), a.Email("alice@example.org")
	if e1 != e2 || !strings.HasSuffix(e1, "@example.org") || strings.Contains(e1, "alice") {
		t.Errorf("email %q %q", e1, e2)
	}
	if got := a.IP("192.0.2.77"); got != "192.0.2.0/24" {
		t.Errorf("ip4 %q", got)
	}
	if got := a.IP("2001:db8:1:2::5"); got != "2001:db8:1::/48" {
		t.Errorf("ip6 %q", got)
	}
	var off *Anonymizer
	if off.Email("a@b.c") != "a@b.c" || off.IP("192.0.2.1") != "192.0.2.1" || off.Enabled() {
		t.Error("nil anonymizer must be identity")
	}
}

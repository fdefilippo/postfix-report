// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package norm normalizes values for aggregation and anonymizes personal
// data for publication.
package norm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"regexp"
	"strings"
	"unicode/utf8"
)

// NullSender is the label used for the null envelope sender "<>".
const NullSender = "(null sender)"

// Domain returns the lower-cased domain of an address. Addresses without "@"
// (local users like "root") are reported as "(local)".
func Domain(addr string) string {
	if addr == "" {
		return NullSender
	}
	i := strings.LastIndexByte(addr, '@')
	if i < 0 || i == len(addr)-1 {
		return "(local)"
	}
	return strings.ToLower(addr[i+1:])
}

var (
	reAngleAddr = regexp.MustCompile(`<[^<>\s]*@[^<>\s]*>`)
	reAddr      = regexp.MustCompile(`[A-Za-z0-9._%+=-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	reHostIP    = regexp.MustCompile(`([A-Za-z0-9.-]+)\[[0-9A-Fa-f.:]+\](:\d+)?`)
	reIPv4      = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reIPv6      = regexp.MustCompile(`\b[0-9A-Fa-f]{1,4}(:[0-9A-Fa-f]{0,4}){2,7}\b`)
	reInReply   = regexp.MustCompile(`\s*\(in reply to [^)]*\)\s*$`)
	reSaid      = regexp.MustCompile(`^host \S+ (said|refused to talk to me):\s*`)
	reLongNum   = regexp.MustCompile(`\b[0-9A-Za-z+/_-]*[0-9][0-9A-Za-z+/_-]{7,}\b`)
	reURL       = regexp.MustCompile(`https?://\S+`)
	reSpace     = regexp.MustCompile(`\s+`)
	reMultiLine = regexp.MustCompile(`\b[245]\d\d[- ][245]\.\d{1,3}\.\d{1,3}\b`)
)

// maxReason is the maximum length of a normalized reason.
const maxReason = 140

// Reason normalizes a reject/deferral/bounce text so that equal causes group
// together and no address or IP is retained: addresses become <addr>,
// "host[ip]:port" becomes "host", IPs become [ip], long identifiers become #.
func Reason(text string) string {
	s := strings.TrimSpace(text)
	s = reSaid.ReplaceAllString(s, "")
	s = reInReply.ReplaceAllString(s, "")
	s = reAngleAddr.ReplaceAllString(s, "<addr>")
	s = reAddr.ReplaceAllString(s, "addr")
	s = reHostIP.ReplaceAllString(s, "$1")
	s = reIPv4.ReplaceAllString(s, "[ip]")
	s = reIPv6.ReplaceAllString(s, "[ip]")
	s = reURL.ReplaceAllString(s, "<url>")
	// Multi-line SMTP replies repeat "550-5.1.1": keep the first occurrence.
	first := true
	s = reMultiLine.ReplaceAllStringFunc(s, func(m string) string {
		if first {
			first = false
			return m
		}
		return ""
	})
	s = reLongNum.ReplaceAllString(s, "#")
	s = reSpace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if s == "" {
		s = "(no text)"
	}
	if len(s) > maxReason {
		s = s[:maxReason]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}

// RelayHost returns the host part of a relay value ("mx.example.org[1.2.3.4]:25"
// -> "mx.example.org"); "none" and transport names are returned unchanged.
func RelayHost(relay string) string {
	if i := strings.IndexByte(relay, '['); i > 0 {
		return strings.ToLower(relay[:i])
	}
	return relay
}

// Anonymizer pseudonymizes addresses and IPs with a keyed hash, so that the
// same value maps to the same token without being reversible from the report.
type Anonymizer struct {
	key   []byte
	email bool
	ip    bool
}

// NewAnonymizer creates an Anonymizer. key must be secret (it is stored in
// the database, never in reports).
func NewAnonymizer(key []byte, email, ip bool) *Anonymizer {
	return &Anonymizer{key: key, email: email, ip: ip}
}

func (a *Anonymizer) token(s string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))[:10]
}

// Email pseudonymizes the local part of an address (the domain is kept).
func (a *Anonymizer) Email(addr string) string {
	if a == nil || !a.email || addr == "" {
		return addr
	}
	i := strings.LastIndexByte(addr, '@')
	if i < 0 {
		return "u-" + a.token(addr)
	}
	return "u-" + a.token(strings.ToLower(addr[:i])) + addr[i:]
}

// User pseudonymizes a SASL username.
func (a *Anonymizer) User(u string) string {
	if a == nil || !a.email || u == "" {
		return u
	}
	return a.Email(u)
}

// IP truncates an address to its network (/24 for IPv4, /48 for IPv6).
func (a *Anonymizer) IP(s string) string {
	if a == nil || !a.ip || s == "" {
		return s
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return "(invalid)"
	}
	if v4 := ip.To4(); v4 != nil {
		return net.IPv4(v4[0], v4[1], v4[2], 0).String() + "/24"
	}
	m := net.CIDRMask(48, 128)
	return ip.Mask(m).String() + "/48"
}

// Enabled reports whether any anonymization is active.
func (a *Anonymizer) Enabled() bool { return a != nil && (a.email || a.ip) }

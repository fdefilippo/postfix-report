// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package testutil

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"sort"
	"time"
)

// SynthConfig describes a synthetic log. All names are fictitious.
type SynthConfig struct {
	Start     time.Time // first day (local midnight)
	Days      int
	PerDay    int    // average accepted messages on a weekday
	Missing   []int  // day indexes without any line (log lost)
	GapDay    int    // day index with a 3-hour hole (-1 = none)
	Seed      uint64 // deterministic output
	Host      string
	FilterUID int // uid of the content-filter reinjection
}

var (
	synthSenders = []string{"noreply@app.example.org", "alerts@monitor.example.org", "news@lists.example.net",
		"billing@shop.example.com", "hr@example.org", "it@example.org", "root@mx1.example.org", ""}
	synthRcptDomains = []string{"example.org", "example.org", "example.org", "example.com", "example.net",
		"mail.example.com", "partner.example.net"}
	synthClients = []string{"192.0.2.10", "192.0.2.11", "192.0.2.12", "198.51.100.20", "198.51.100.21", "203.0.113.30"}
	synthRelays  = map[string]string{"example.org": "store.example.org[192.0.2.50]:25", "example.com": "mx.example.com[198.51.100.80]:25",
		"example.net": "mx.example.net[203.0.113.80]:25", "mail.example.com": "mail.example.com[198.51.100.81]:25",
		"partner.example.net": "mx.partner.example.net[203.0.113.81]:25"}
)

// Synthetic writes a synthetic Postfix log to w.
func Synthetic(w io.Writer, c SynthConfig) error {
	if c.Host == "" {
		c.Host = "mx1"
	}
	if c.FilterUID == 0 {
		c.FilterUID = 97
	}
	rng := rand.New(rand.NewPCG(c.Seed, c.Seed^0x9e3779b97f4a7c15))
	bw := bufio.NewWriterSize(w, 1<<16)
	missing := map[int]bool{}
	for _, d := range c.Missing {
		missing[d] = true
	}
	type line struct {
		t time.Time
		s string
	}
	var qseq uint64 = 0x100000
	qid := func() string { qseq += 1 + uint64(rng.IntN(7)); return fmt.Sprintf("%X", 0xA000000000+qseq) }
	pid := 4000
	var carry []line
	for d := 0; d < c.Days; d++ {
		day := c.Start.AddDate(0, 0, d)
		if missing[d] {
			carry = nil
			continue
		}
		var lines []line
		emit := func(t time.Time, proc, msg string) {
			pid = 4000 + rng.IntN(5000)
			lines = append(lines, line{t, fmt.Sprintf("%s %s postfix/%s[%d]: %s", t.Format(time.Stamp), c.Host, proc, pid, msg)})
		}
		// Heartbeat lines keep coverage continuous (anvil statistics every 10 min).
		for m := 0; m < 24*60; m += 10 {
			emit(day.Add(time.Duration(m)*time.Minute), "anvil", "statistics: max cache size 1 at "+day.Format(time.Stamp))
		}
		wd := day.Weekday()
		volume := float64(c.PerDay)
		if wd == time.Saturday || wd == time.Sunday {
			volume *= 0.3
		}
		volume *= 0.85 + 0.3*rng.Float64()
		n := int(volume)
		for i := 0; i < n; i++ {
			// Office-hours profile.
			h := officeHour(rng)
			t := day.Add(time.Duration(h*3600+rng.IntN(3600)) * time.Second)
			sender := synthSenders[rng.IntN(len(synthSenders)-1)]
			client := synthClients[rng.IntN(len(synthClients))]
			q, rq := qid(), qid()
			nr := 1
			if rng.IntN(10) == 0 {
				nr = 2 + rng.IntN(4)
			}
			size := 800 + int(math.Exp(rng.Float64()*9))
			mid := fmt.Sprintf("%d.%s@app.example.org", t.Unix(), q)
			emit(t, "smtpd", fmt.Sprintf("connect from client.example.org[%s]", client))
			if rng.IntN(60) == 0 {
				emit(t, "smtpd", fmt.Sprintf("%s: client=client.example.org[%s]", q, client))
				emit(t, "cleanup", fmt.Sprintf("%s: milter-reject: END-OF-MESSAGE from client.example.org[%s]: 4.7.1 Try again later; from=<%s> to=<u@example.org> proto=ESMTP helo=<client.example.org>", q, client, sender))
				emit(t.Add(time.Second), "smtpd", fmt.Sprintf("disconnect from client.example.org[%s] ehlo=1 mail=1 rcpt=1 data=0/1 quit=1 commands=4/5", client))
				continue
			}
			sasl := ""
			if rng.IntN(8) == 0 {
				sasl = ", sasl_method=LOGIN, sasl_username=app" + fmt.Sprint(rng.IntN(4))
			}
			emit(t, "smtpd", fmt.Sprintf("%s: client=client.example.org[%s]%s", q, client, sasl))
			emit(t, "cleanup", fmt.Sprintf("%s: message-id=<%s>", q, mid))
			emit(t, "qmgr", fmt.Sprintf("%s: from=<%s>, size=%d, nrcpt=%d (queue active)", q, sender, size, nr))
			emit(t.Add(time.Second), "smtpd", fmt.Sprintf("disconnect from client.example.org[%s] ehlo=2 starttls=1 mail=1 rcpt=%d data=1 quit=1 commands=%d", client, nr, 6+nr))
			t1 := t.Add(time.Second)
			emit(t1, "pickup", fmt.Sprintf("%s: uid=%d from=<%s>", rq, c.FilterUID, sender))
			emit(t1, "cleanup", fmt.Sprintf("%s: message-id=<%s>", rq, mid))
			emit(t1, "qmgr", fmt.Sprintf("%s: from=<%s>, size=%d, nrcpt=%d (queue active)", rq, sender, size+90, nr))
			var rcpts []string
			seen := map[string]bool{}
			for r := 0; r < nr; r++ {
				dom := synthRcptDomains[rng.IntN(len(synthRcptDomains))]
				rc := fmt.Sprintf("user%d@%s", rng.IntN(500), dom)
				for seen[rc] { // Postfix removes duplicate recipients
					rc = fmt.Sprintf("user%d@%s", rng.IntN(500), dom)
				}
				seen[rc] = true
				rcpts = append(rcpts, rc)
				emit(t1, "pipe", fmt.Sprintf("%s: to=<%s>, relay=filter, delay=0.3, delays=0.2/0/0/0.1, dsn=2.0.0, status=sent (delivered via filter service)", q, rc))
			}
			emit(t1, "qmgr", q+": removed")
			last := t1
			for _, rc := range rcpts {
				dom := domainOf(rc)
				relay := synthRelays[dom]
				dt := t1.Add(time.Duration(1+rng.IntN(3)) * time.Second)
				switch x := rng.IntN(100); {
				case x < 4: // deferred, then delivered 10-60 minutes later
					emit(dt, "smtp", fmt.Sprintf("%s: to=<%s>, relay=none, delay=30, delays=0/0/30/0, dsn=4.4.1, status=deferred (connect to %s: Connection timed out)", rq, rc, relay))
					later := dt.Add(time.Duration(10+rng.IntN(50)) * time.Minute)
					emit(later, "qmgr", fmt.Sprintf("%s: from=<%s>, size=%d, nrcpt=%d (queue active)", rq, sender, size+90, nr))
					emit(later.Add(time.Second), "smtp", fmt.Sprintf("%s: to=<%s>, relay=%s, delay=%d, delays=%d/0/0.4/0.6, dsn=2.0.0, status=sent (250 2.0.0 Ok)", rq, rc, relay, int(later.Sub(t1).Seconds())+1, int(later.Sub(t1).Seconds())))
					if later.After(last) {
						last = later.Add(time.Second)
					}
				case x < 7: // bounced, with notification
					emit(dt, "smtp", fmt.Sprintf("%s: to=<%s>, relay=%s, delay=1.2, delays=0.1/0/0.5/0.6, dsn=5.1.1, status=bounced (host %s said: 550 5.1.1 <%s>: Recipient address rejected: User unknown (in reply to RCPT TO command))", rq, rc, relay, relay[:len(relay)-3], rc))
					nq := qid()
					emit(dt, "cleanup", fmt.Sprintf("%s: message-id=<%d.%s@mx1.example.org>", nq, dt.Unix(), nq))
					emit(dt, "bounce", fmt.Sprintf("%s: sender non-delivery notification: %s", rq, nq))
					emit(dt, "qmgr", fmt.Sprintf("%s: from=<>, size=%d, nrcpt=1 (queue active)", nq, 2000+size/10))
					emit(dt.Add(time.Second), "lmtp", fmt.Sprintf("%s: to=<%s>, relay=mx1.example.org[private/dovecot-lmtp], delay=0.1, delays=0/0/0/0.1, dsn=2.0.0, status=sent (250 2.0.0 Saved)", nq, orNull(sender)))
					emit(dt.Add(time.Second), "qmgr", nq+": removed")
				default:
					delay := 0.3 + rng.ExpFloat64()*1.2
					emit(dt, "smtp", fmt.Sprintf("%s: to=<%s>, relay=%s, delay=%.2f, delays=0.01/0/0.3/%.2f, dsn=2.0.0, status=sent (250 2.0.0 Ok: queued)", rq, rc, relay, delay, delay-0.31))
				}
				if dt.After(last) {
					last = dt
				}
			}
			emit(last.Add(time.Second), "qmgr", rq+": removed")
		}
		// Rejections without queue ID and SASL failures.
		for i := 0; i < n/25+rng.IntN(5); i++ {
			t := day.Add(time.Duration(rng.IntN(86400)) * time.Second)
			emit(t, "smtpd", fmt.Sprintf("NOQUEUE: reject: RCPT from unknown[203.0.113.%d]: 554 5.7.1 <x%d@example.com>: Relay access denied; from=<spam@example.com> to=<x%d@example.com> proto=ESMTP helo=<spam>", 100+rng.IntN(50), i, i))
		}
		for i := 0; i < n/40+rng.IntN(5); i++ {
			t := day.Add(time.Duration(rng.IntN(86400)) * time.Second)
			emit(t, "smtpd", fmt.Sprintf("warning: unknown[203.0.113.%d]: SASL LOGIN authentication failed: UGFzc3dvcmQ6", 150+rng.IntN(20)))
		}
		lines = append(lines, carry...)
		carry = nil
		sort.SliceStable(lines, func(i, j int) bool { return lines[i].t.Before(lines[j].t) })
		next := day.AddDate(0, 0, 1)
		for _, l := range lines {
			if !l.t.Before(next) {
				// Late deliveries spill into the next day's log.
				if !missing[d+1] {
					carry = append(carry, l)
				}
				continue
			}
			if d == c.GapDay {
				// A 3-hour hole in the log (e.g. syslog down), 13:00-16:00.
				if h := l.t.Hour(); h >= 13 && h < 16 {
					continue
				}
			}
			bw.WriteString(l.s)
			bw.WriteByte('\n')
		}
	}
	return bw.Flush()
}

func orNull(s string) string {
	if s == "" {
		return "postmaster@example.org"
	}
	return s
}

func domainOf(a string) string {
	for i := len(a) - 1; i >= 0; i-- {
		if a[i] == '@' {
			return a[i+1:]
		}
	}
	return ""
}

func officeHour(rng *rand.Rand) int {
	// Weighted: night low, peaks at 10-12 and 15-16.
	w := []int{2, 1, 1, 1, 1, 2, 4, 8, 14, 18, 20, 19, 12, 14, 17, 16, 12, 8, 5, 4, 3, 3, 2, 2}
	sum := 0
	for _, x := range w {
		sum += x
	}
	r := rng.IntN(sum)
	for h, x := range w {
		if r < x {
			return h
		}
		r -= x
	}
	return 12
}

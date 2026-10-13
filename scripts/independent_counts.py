#!/usr/bin/env python3
# Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
# SPDX-License-Identifier: GPL-3.0-or-later
"""Independent recount of the main postfix-report metrics from a raw maillog.

This script deliberately shares no code or logic with the Go implementation:
it works on the raw text, attributes each line to the local day written in the
log (no timezone conversion) and uses a minimal per-Queue-ID state machine.
It is used to produce VALIDATION.md.

Usage: independent_counts.py MAILLOG [--reinject-uid 97] [--filter-relay filter]
Output: JSON with per-day and total counts.
"""
import argparse
import collections
import json
import math
import re
import sys

MONTHS = {m: i for i, m in enumerate(
    "Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec".split(), 1)}

HDR = re.compile(r'^(\w{3}) +(\d+) (\d\d):\d\d:\d\d \S+ postfix(?:-[\w-]+)?/(?:[\w-]+/)?([\w-]+)\[\d+\]: (.*)$')
QID = re.compile(r'^([0-9A-F]{6,12}|[0-9A-Za-z]{12,20}): (.*)$')


def nearest_rank(sorted_vals, p):
    if not sorted_vals:
        return None
    i = max(0, math.ceil(p / 100 * len(sorted_vals)) - 1)
    return sorted_vals[i]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("maillog")
    ap.add_argument("--year", type=int, default=2026)
    ap.add_argument("--reinject-uid", type=int, default=97)
    ap.add_argument("--filter-relay", default="filter")
    args = ap.parse_args()

    days = collections.defaultdict(collections.Counter)
    hours = collections.defaultdict(collections.Counter)
    delays = collections.defaultdict(list)
    sender_dom = collections.defaultdict(collections.Counter)
    client_ip = collections.defaultdict(collections.Counter)
    # Per Queue ID: (origin, accepted?, client ip) for the current lifecycle.
    state = {}
    # Per Queue ID: recipient -> last status, for expiry accounting.
    last_status = collections.defaultdict(dict)

    with open(args.maillog, encoding="utf-8", errors="replace") as f:
        for line in f:
            m = HDR.match(line.rstrip("\n"))
            if not m:
                continue
            mon, dd, hh, proc, msg = m.groups()
            day = "%04d-%02d-%02d" % (args.year, MONTHS[mon], int(dd))
            hour = int(hh)
            D = days[day]

            def inc(key, n=1):
                D[key] += n
                hours[(day, hour)][key] += n

            if proc == "smtpd":
                if msg.startswith("connect from "):
                    inc("connections")
                elif msg.startswith("disconnect from "):
                    inc("sessions")
                    mm = re.search(r' starttls=(\d+)', msg)
                    if mm and int(mm.group(1)) > 0:
                        inc("sessions_starttls")
                elif "authentication failed" in msg and msg.startswith("warning:"):
                    inc("sasl_fail")
                elif msg.startswith("NOQUEUE: reject: "):
                    inc("rejects")
                    code = re.search(r'\]: (\d)\d\d ', msg)
                    inc("rejects_temp" if code and code.group(1) == "4" else "rejects_perm")
            qm = QID.match(msg)
            if not qm:
                continue
            q, rest = qm.groups()
            if proc == "smtpd" and rest.startswith("client="):
                ip = re.search(r'\[([^\]]+)\]', rest).group(1)
                state[q] = ["smtp", False, ip, "sasl_username=" in rest]
            elif proc == "pickup" and rest.startswith("uid="):
                uid = int(re.match(r'uid=(\d+)', rest).group(1))
                state[q] = ["reinject" if uid == args.reinject_uid else "local", False, None, False]
            elif proc == "cleanup" and "milter-reject:" in rest:
                inc("rejects")
                dsn = re.search(r'\]: (\d)\.\d+\.\d+ ', rest)
                inc("rejects_temp" if dsn and dsn.group(1) == "4" else "rejects_perm")
                state.pop(q, None)
            elif proc == "qmgr" and rest.endswith("(queue active)"):
                st = state.get(q)
                if st and not st[1]:
                    st[1] = True
                    if st[0] in ("smtp", "local"):
                        size = int(re.search(r'size=(\d+)', rest).group(1))
                        nrcpt = int(re.search(r'nrcpt=(\d+)', rest).group(1))
                        sender = re.search(r'from=<([^>]*)>', rest).group(1)
                        dom = sender.rsplit("@", 1)[1].lower() if "@" in sender else ("(null sender)" if sender == "" else "(local)")
                        inc("msg_accepted")
                        inc("msg_" + st[0])
                        inc("rcpt_accepted", nrcpt)
                        inc("bytes_accepted", size)
                        sender_dom[day][dom] += 1
                        if st[0] == "smtp":
                            client_ip[day][st[2]] += 1
                            if st[3]:
                                inc("sasl_ok")
            elif proc == "qmgr" and rest == "removed":
                state.pop(q, None)
                last_status.pop(q, None)
            elif proc == "qmgr" and "status=expired" in rest:
                inc("expired_msg")
                pend = sum(1 for s in last_status[q].values() if s == "deferred")
                inc("expired_rcpt", pend)
            elif proc == "bounce" and " notification: " in rest:
                inc("notifications")
            elif rest.startswith("to=<") and "status=" in rest:
                status = re.search(r'status=(\w+)', rest).group(1)
                relay = re.search(r'relay=([^,]+)', rest).group(1)
                rcpt = re.search(r'to=<([^>]*)>', rest).group(1)
                if relay == args.filter_relay:
                    inc("filter_handoffs")
                    continue
                if status in ("deliverable", "undeliverable"):
                    continue
                last_status[q][rcpt] = status
                if status == "sent":
                    inc("sent")
                    delays[day].append(float(re.search(r'delay=([\d.]+)', rest).group(1)))
                elif status == "deferred":
                    inc("deferred")
                elif status == "bounced":
                    inc("bounced")

    out = {"days": {}, "total": collections.Counter(), "hours": {}}
    all_delays = []
    for day in sorted(days):
        c = dict(days[day])
        c["final_failures"] = c.get("bounced", 0) + c.get("expired_rcpt", 0)
        d = sorted(delays[day])
        all_delays += d
        c["delay"] = delay_stats(d)
        c["top_sender_domains"] = sender_dom[day].most_common(5)
        c["top_client_ips"] = client_ip[day].most_common(5)
        out["days"][day] = c
        out["total"].update({k: v for k, v in c.items() if isinstance(v, (int, float))})
    out["total"] = dict(out["total"])
    out["total"]["delay"] = delay_stats(sorted(all_delays))
    out["hours"] = {"%s %02d" % k: dict(v) for k, v in sorted(hours.items())}
    json.dump(out, sys.stdout, indent=1, sort_keys=True)


def delay_stats(d):
    if not d:
        return {"count": 0}
    return {"count": len(d), "avg": round(sum(d) / len(d), 6), "p50": nearest_rank(d, 50), "p90": nearest_rank(d, 90),
            "p95": nearest_rank(d, 95), "p99": nearest_rank(d, 99), "max": d[-1]}


if __name__ == "__main__":
    main()

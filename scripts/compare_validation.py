#!/usr/bin/env python3
# Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
# SPDX-License-Identifier: GPL-3.0-or-later
"""Compare postfix-report results with the independent recount.

Usage: compare_validation.py BINARY CONFIG INDEPENDENT_JSON
Prints Markdown tables (used in VALIDATION.md) and exits 1 on any mismatch.
"""
import json
import sqlite3
import subprocess
import sys

METRICS = ["msg_accepted", "msg_smtp", "msg_local", "rcpt_accepted", "bytes_accepted", "sent", "deferred",
           "bounced", "expired_rcpt", "expired_msg", "final_failures", "rejects", "rejects_temp", "rejects_perm",
           "notifications", "filter_handoffs", "connections", "sessions", "sessions_starttls", "sasl_ok", "sasl_fail"]
DELAY = ["count", "avg", "p50", "p90", "p95", "p99", "max"]


def stats(binary, config, day_from=None, day_to=None):
    cmd = [binary, "-config", config, "stats", "--json"]
    if day_from:
        cmd += ["--from", day_from, "--to", day_to]
    return json.loads(subprocess.check_output(cmd))


def main():
    binary, config, indep_path = sys.argv[1:4]
    indep = json.load(open(indep_path))
    mismatches = 0
    days = sorted(indep["days"])
    cols = days + ["totale"]
    prog = {d: stats(binary, config, d, d) for d in days}
    prog["totale"] = stats(binary, config)
    ind = dict(indep["days"])
    ind["totale"] = indep["total"]

    print("| Metrica | " + " | ".join(f"{c} programma | {c} indipendente" for c in cols) + " | Esito |")
    print("|---|" + "---:|---:|" * len(cols) + "---|")
    for m in METRICS:
        row, ok = [], True
        for c in cols:
            p = prog[c]["metrics"].get(m, 0)
            i = ind[c].get(m, 0)
            ok &= abs(p - i) < 1e-6
            row += [fmt(p), fmt(i)]
        mismatches += not ok
        print(f"| {m} | " + " | ".join(row) + f" | {'OK' if ok else '**DIFF**'} |")

    print()
    print("| Ritardo consegne (s) | " + " | ".join(f"{c} programma | {c} indipendente" for c in cols) + " | Esito |")
    print("|---|" + "---:|---:|" * len(cols) + "---|")
    for k in DELAY:
        row, ok = [], True
        for c in cols:
            p = pick(prog[c]["delivery_delay"], k)
            i = ind[c]["delay"].get(k)
            ok &= i is not None and abs(p - i) < 1e-4
            row += [fmt(p), fmt(i)]
        mismatches += not ok
        print(f"| {k} | " + " | ".join(row) + f" | {'OK' if ok else '**DIFF**'} |")

    # Hourly comparison straight from the aggregate table.
    db = sqlite3.connect(prog_db(config))
    hourly = {}
    for day, hour, metric, value in db.execute("SELECT day, hour, metric, value FROM agg_hour"):
        hourly.setdefault(f"{day} {hour:02d}", {})[metric] = value
    hdiff, checked = [], 0
    for key, vals in indep["hours"].items():
        for m in ("msg_accepted", "sent", "deferred", "bounced", "rejects", "connections", "sasl_fail"):
            checked += 1
            if abs(hourly.get(key, {}).get(m, 0) - vals.get(m, 0)) > 1e-6:
                hdiff.append(f"{key} {m}: programma {hourly.get(key, {}).get(m, 0)}, indipendente {vals.get(m, 0)}")
    print()
    print(f"Confronto orario: {checked} valori (ora × metrica) confrontati, {len(hdiff)} differenze.")
    for d in hdiff[:20]:
        print("  -", d)
    mismatches += len(hdiff)

    # Rankings: top 5 sender domains and client IPs per day (counts only).
    rdiff = 0
    for day in days:
        for dim, key in (("sender_domain", "top_sender_domains"), ("client_ip", "top_client_ips")):
            got = db.execute("SELECT key, value FROM agg_dim WHERE day=? AND dim=? ORDER BY value DESC, key LIMIT 5",
                             (day, dim)).fetchall()
            exp = indep["days"][day][key]
            if sorted(v for _, v in got) != sorted(v for _, v in exp) or {k for k, _ in got} != {k for k, _ in exp}:
                # Ties at the 5th position may legitimately differ in key order.
                if [v for _, v in got] != sorted((v for _, v in exp), reverse=True):
                    rdiff += 1
                    print(f"  - classifica {dim} {day}: programma {got} indipendente {exp}")
    print(f"Classifiche (top 5 domini mittenti e IP sorgenti per giorno): {2 * len(days)} confronti, {rdiff} differenze.")
    mismatches += rdiff
    print()
    print("ESITO:", "nessuna differenza" if mismatches == 0 else f"{mismatches} differenze")
    sys.exit(1 if mismatches else 0)


def pick(d, k):
    return {"count": d["Count"], "avg": d["Avg"], "p50": d["P50"], "p90": d["P90"], "p95": d["P95"],
            "p99": d["P99"], "max": d["Max"]}[k]


def prog_db(config):
    for line in open(config):
        line = line.strip()
        if line.startswith("path:"):
            return line.split(":", 1)[1].strip()
    raise SystemExit("database.path not found in config")


def fmt(v):
    if v is None:
        return "—"
    if isinstance(v, float) and not v.is_integer():
        return f"{v:.4f}".rstrip("0")
    return f"{int(v):,}".replace(",", ".")


if __name__ == "__main__":
    main()

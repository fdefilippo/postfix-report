# Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
# SPDX-License-Identifier: GPL-3.0-or-later
"""Exercise the comparator CLI with synthetic stats and SQLite."""
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest


class ComparatorCLITest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.cfg = root / "config.yaml"
        self.cfg.write_text(f"database:\n  path: {root / 'data.db'}\n")
        self.independent = root / "independent.json"
        domains = {f"sender{i}.example.org": 2 for i in range(8)}
        ips = {"192.0.2.1": 16}
        delay = {k: 1 for k in ("count", "avg", "p50", "p90", "p95", "p99", "max")}
        day = dict(msg_accepted=16, sent=1, sender_domains=domains, client_ips=ips, delay=delay)
        self.independent.write_text(json.dumps(dict(days={"2026-10-12": day}, total=day, hours={})))
        payload = dict(metrics={"msg_accepted": 16, "sent": 1},
                       delivery_delay={"Count": 1, "Avg": 1, "P50": 1, "P90": 1, "P95": 1, "P99": 1, "Max": 1})
        self.binary = root / "stats-fixture"
        self.binary.write_text("#!/usr/bin/env python3\nprint(" + repr(json.dumps(payload)) + ")\n")
        self.binary.chmod(0o700)
        self.db = sqlite3.connect(root / "data.db")
        self.addCleanup(self.db.close)
        self.db.executescript("CREATE TABLE agg_hour(day,hour,metric,value); CREATE TABLE agg_dim(day,dim,key,value);")
        for dim, values in (("sender_domain", domains), ("client_ip", ips)):
            self.db.executemany("INSERT INTO agg_dim VALUES('2026-10-12',?,?,?)", [(dim,k,v) for k,v in values.items()])
        self.db.commit()

    def compare(self):
        self.db.commit()
        return subprocess.run([sys.executable, "-I", str(Path(__file__).with_name("compare_validation.py")),
                               str(self.binary), str(self.cfg), str(self.independent)],
                              capture_output=True, text=True, check=False)

    def test_tied_rankings_pass(self):
        # Eight equal values straddle the old top-five cutoff.
        result = self.compare()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("nessuna differenza", result.stdout)

    def test_key_corruption_fails(self):
        self.db.execute("UPDATE agg_dim SET key='wrong.example.org' WHERE key='sender0.example.org'")
        self.assertEqual(self.compare().returncode, 1)

    def test_below_cutoff_corruption_fails(self):
        self.db.execute("UPDATE agg_dim SET key='wrong.example.org' WHERE key='sender7.example.org'")
        self.assertEqual(self.compare().returncode, 1)

    def test_balanced_count_corruption_fails(self):
        self.db.execute("UPDATE agg_dim SET value=value+CASE key WHEN 'sender0.example.org' THEN 1 ELSE -1 END WHERE key IN ('sender0.example.org','sender1.example.org')")
        self.assertEqual(self.compare().returncode, 1)

    def test_extra_row_fails(self):
        self.db.execute("INSERT INTO agg_dim VALUES('2026-10-12','sender_domain','extra.example.org',0)")
        self.assertEqual(self.compare().returncode, 1)

    def test_missing_row_fails(self):
        self.db.execute("DELETE FROM agg_dim WHERE key='sender0.example.org'")
        self.assertEqual(self.compare().returncode, 1)

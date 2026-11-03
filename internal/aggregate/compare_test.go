// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package aggregate_test

import (
	"context"
	"github.com/fdefilippo/postfix-report/internal/aggregate"
	"github.com/fdefilippo/postfix-report/internal/testutil"
	"testing"
	"time"
)

func TestCompareDimensionIdentity(t *testing.T) {
	for name, mutation := range map[string]string{
		"baseline": "", "key": `UPDATE agg_dim SET key=key||'-wrong' WHERE dim='sender_domain'`,
		"dimension":       `UPDATE agg_dim SET dim='wrong' WHERE dim='sender_domain'`,
		"balanced counts": `UPDATE agg_dim SET value=value+CASE WHEN key='example.org' THEN 1 ELSE -1 END WHERE dim='sender_domain'`,
		"missing":         `DELETE FROM agg_dim WHERE dim='sender_domain'`,
		"extra zero":      `INSERT INTO agg_dim(day,dim,key,value) VALUES('2026-10-12','sender_domain','extra',0)`,
	} {
		t.Run(name, func(t *testing.T) {
			env := testutil.NewEnv(t)
			a := env.Open(t)
			defer a.Close()
			l := testutil.NewLog(t).SMTPMessage("2026-10-12 10:00:00", "A111111111", "B111111111", "a@example.org", 100, "r@example.org").SMTPMessage("2026-10-12 11:00:00", "A222222222", "B222222222", "b@example.net", 100, "r@example.org")
			path := env.LogPath("mail")
			testutil.WriteFile(t, path, l.String(), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
			if _, err := a.Import(context.Background(), "test", []string{path}); err != nil {
				t.Fatal(err)
			}
			if mutation != "" {
				if _, err := a.DB.Exec(mutation); err != nil {
					t.Fatal(err)
				}
			}
			ag := aggregate.Aggregator{DB: a.DB, Loc: env.Cfg.Location}
			diffs, err := ag.Compare("2026-10-12")
			if err != nil {
				t.Fatal(err)
			}
			if (len(diffs) > 0) != (mutation != "") {
				t.Fatalf("diffs=%v for mutation %q", diffs, mutation)
			}
		})
	}
}

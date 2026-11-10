// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDisabledReportIsDeniedBeforePublicationUpdate(t *testing.T) {
	e, _, ts, ptr := serverFixture(t, "/postfix-report/")
	b := newBrowser(t, ts, "/postfix-report/")
	if code, _ := b.call("GET", "/monthly/2026-10.html", nil); code != 200 {
		t.Fatalf("baseline %d", code)
	}
	next := *e.Cfg
	next.Reports.Monthly = false
	ptr.Store(&next)
	// Physical retirement belongs to update/generate. The daemon must apply the
	// configuration immediately even while old HTML is still on disk.
	if _, err := os.Stat(filepath.Join(e.Cfg.Output.Directory, "monthly/2026-10.html")); err != nil {
		t.Fatal(err)
	}
	if code, _ := b.call("GET", "/monthly/2026-10.html", nil); code != 404 {
		t.Fatalf("disabled HTML=%d", code)
	}
	if code, _ := b.call("GET", "/daily/2026-10-25.html", nil); code != 200 {
		t.Fatalf("enabled HTML=%d", code)
	}
	code, data := b.call("POST", "/api/exports", []byte(`{"kind":"month","key":"2026-10","format":"odt"}`))
	if code != 202 {
		t.Fatalf("export queue=%d", code)
	}
	var job map[string]string
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	id := job["id"]
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		code, data = b.call("GET", "/api/exports/"+id, nil)
		if code != 200 {
			t.Fatalf("job=%d", code)
		}
		if err := json.Unmarshal(data, &job); err != nil {
			t.Fatal(err)
		}
		if job["state"] == "ready" {
			t.Fatal("disabled report was exported")
		}
		if job["state"] == "failed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("disabled export did not fail within deadline")
}

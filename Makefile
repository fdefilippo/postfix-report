# Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
# SPDX-License-Identifier: GPL-3.0-or-later

BINARY   := postfix-report
PREFIX   ?= /usr/local
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
GO       ?= go
# Static, CGO-free binary: runs on any Linux of the same architecture
# (including Enterprise Linux 8/9/10) without SQLite libraries.
export CGO_ENABLED := 0

REAL_LOG ?= $(firstword $(wildcard samples/maillog) $(wildcard maillog))

# RPM packaging (Enterprise Linux): the version comes from the spec file.
RPM_SPEC       := packaging/rpm/$(BINARY).spec
RPM_VERSION    := $(shell awk '/^Version:/ {print $$2; exit}' $(RPM_SPEC))
DIST_NAME      := $(BINARY)-$(RPM_VERSION)
RPM_TOPDIR     ?= $(CURDIR)/dist/rpmbuild
RPMBUILD_FLAGS ?=

.PHONY: all build test test-short test-race bench vet fmt-check check validate demo install install-systemd uninstall dist rpm srpm clean

all: check build

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/postfix-report

# Full test suite (includes the reference-log regression tests when
# samples/maillog or ./maillog exists, and headless-browser tests when
# chromium is installed).
test:
	$(GO) test ./...

test-short:
	$(GO) test -short ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race -short ./...

bench:
	$(GO) test -run '^$$' -bench . -benchmem ./internal/logparse/ ./internal/app/

vet:
	$(GO) vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w ."; exit 1; }

check: fmt-check vet test

# Independent validation on the reference log (see VALIDATION.md).
validate: build
	@test -n "$(REAL_LOG)" || { echo "reference log not found (samples/maillog)"; exit 1; }
	@mkdir -p $(HOME)/tmp/postfix-report-validate
	@printf 'database:\n  path: %s/tmp/postfix-report-validate/report.db\ninput:\n  year: 2026\n  timezone: Europe/Rome\noutput:\n  directory: %s/tmp/postfix-report-validate/www\n' "$(HOME)" "$(HOME)" > $(HOME)/tmp/postfix-report-validate/config.yaml
	@rm -f $(HOME)/tmp/postfix-report-validate/report.db*
	bin/$(BINARY) -config $(HOME)/tmp/postfix-report-validate/config.yaml import --file $(REAL_LOG)
	bin/$(BINARY) -config $(HOME)/tmp/postfix-report-validate/config.yaml verify
	python3 -I scripts/independent_counts.py $(REAL_LOG) > $(HOME)/tmp/postfix-report-validate/independent.json
	python3 -I scripts/compare_validation.py bin/$(BINARY) $(HOME)/tmp/postfix-report-validate/config.yaml $(HOME)/tmp/postfix-report-validate/independent.json

# Demonstration reports from synthetic (fictitious) data, committed in demo/.
demo: build
	@rm -rf $(HOME)/tmp/postfix-report-demo && mkdir -p $(HOME)/tmp/postfix-report-demo/log
	$(GO) run ./cmd/synthlog > $(HOME)/tmp/postfix-report-demo/log/maillog
	touch -d '2026-02-14 12:00' $(HOME)/tmp/postfix-report-demo/log/maillog
	@printf 'database:\n  path: %s/tmp/postfix-report-demo/report.db\ninput:\n  paths: [%s/tmp/postfix-report-demo/log/maillog]\n  timezone: Europe/Rome\noutput:\n  directory: %s/demo\n  title: Demo postfix-report (dati sintetici)\n' "$(HOME)" "$(HOME)" "$(CURDIR)" > $(HOME)/tmp/postfix-report-demo/config.yaml
	rm -rf demo
	bin/$(BINARY) -config $(HOME)/tmp/postfix-report-demo/config.yaml update
	bin/$(BINARY) -config $(HOME)/tmp/postfix-report-demo/config.yaml verify

install: build
	install -D -m 0755 bin/$(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	install -D -m 0640 config.example.yaml $(DESTDIR)/etc/postfix-report/config.yaml.example
	install -D -m 0644 README.md $(DESTDIR)$(PREFIX)/share/doc/postfix-report/README.md
	install -D -m 0644 METRICS.md $(DESTDIR)$(PREFIX)/share/doc/postfix-report/METRICS.md
	install -D -m 0644 LIMITATIONS.md $(DESTDIR)$(PREFIX)/share/doc/postfix-report/LIMITATIONS.md

# The unit refers to /usr/bin (RPM layout): rewrite it for $(PREFIX).
install-systemd:
	install -d $(DESTDIR)/etc/systemd/system
	sed 's|/usr/bin/$(BINARY)|$(PREFIX)/bin/$(BINARY)|' deploy/systemd/postfix-report.service > $(DESTDIR)/etc/systemd/system/postfix-report.service
	chmod 0644 $(DESTDIR)/etc/systemd/system/postfix-report.service
	install -D -m 0644 deploy/systemd/postfix-report.timer $(DESTDIR)/etc/systemd/system/postfix-report.timer

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	rm -rf $(DESTDIR)$(PREFIX)/share/doc/postfix-report

# Source tarball for rpmbuild: files tracked by git (working-tree content,
# demo/ and dot-directories excluded) plus vendor/, so the RPM build needs no network.
dist:
	@test -n "$(RPM_VERSION)" || { echo "Version not found in $(RPM_SPEC)"; exit 1; }
	rm -rf dist/$(DIST_NAME) dist/$(DIST_NAME).tar.gz && mkdir -p dist/$(DIST_NAME)
	git ls-files -z -- . ':!:demo' ':!:.*' | tar --null -T - -cf - | tar -xf - -C dist/$(DIST_NAME)
	cp $(RPM_SPEC) dist/$(DIST_NAME)/
	cd dist/$(DIST_NAME) && $(GO) mod vendor
	tar -C dist --owner=0 --group=0 --sort=name -czf dist/$(DIST_NAME).tar.gz $(DIST_NAME)
	rm -rf dist/$(DIST_NAME)
	@echo dist/$(DIST_NAME).tar.gz

# Binary and source RPMs in $(RPM_TOPDIR)/RPMS and SRPMS. Build on EL9/EL10
# with golang >= 1.25 and rpm-build installed (or use the SRPM with mock).
rpm: dist
	mkdir -p $(RPM_TOPDIR)/SOURCES
	cp dist/$(DIST_NAME).tar.gz $(RPM_TOPDIR)/SOURCES/
	rpmbuild -ba --define '_topdir $(RPM_TOPDIR)' $(RPMBUILD_FLAGS) $(RPM_SPEC)
	@find $(RPM_TOPDIR)/RPMS $(RPM_TOPDIR)/SRPMS -name '$(DIST_NAME)-*.rpm'

srpm: dist
	mkdir -p $(RPM_TOPDIR)/SOURCES
	cp dist/$(DIST_NAME).tar.gz $(RPM_TOPDIR)/SOURCES/
	rpmbuild -bs --define '_topdir $(RPM_TOPDIR)' $(RPMBUILD_FLAGS) $(RPM_SPEC)

clean:
	rm -rf bin dist

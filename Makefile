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
# Baseline x86-64: the Go toolchain of EL9/EL10 defaults to v2/v3.
export GOAMD64 ?= v1

REAL_LOG ?= $(firstword $(wildcard samples/maillog) $(wildcard maillog))

# RPM packaging (Enterprise Linux): the version comes from the spec file.
RPM_SPEC       := packaging/rpm/$(BINARY).spec
RPM_VERSION    := $(shell awk '/^Version:/ {print $$2; exit}' $(RPM_SPEC))
DIST_NAME      := $(BINARY)-$(RPM_VERSION)
RPM_TOPDIR     ?= $(CURDIR)/dist/rpmbuild
RPMBUILD_FLAGS ?=

# Debian package, assembled with dpkg-deb around the static binary
# (DEB_ARCH=amd64 or arm64; Debian's own Go is too old for this module).
DEB_ARCH    ?= amd64
DEB_VERSION := $(RPM_VERSION)-1
DEB_ROOT    := dist/deb/$(DEB_ARCH)/root
DEB_DOC     := $(DEB_ROOT)/usr/share/doc/$(BINARY)
DEB_FILE    := dist/$(BINARY)_$(DEB_VERSION)_$(DEB_ARCH).deb
DEB_DATE    := $(shell git log -1 --format=%cD 2>/dev/null || date -R)

.PHONY: all build test test-short test-race bench vet fmt-check check validate demo install install-systemd uninstall dist rpm srpm deb release-check clean

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
	install -D -m 0644 docs/postfix-report.1 $(DESTDIR)$(PREFIX)/share/man/man1/postfix-report.1
	install -D -m 0644 config.example.yaml $(DESTDIR)$(PREFIX)/share/doc/postfix-report/config.example.yaml
	install -D -m 0644 README.md $(DESTDIR)$(PREFIX)/share/doc/postfix-report/README.md
	install -D -m 0644 README.en.md $(DESTDIR)$(PREFIX)/share/doc/postfix-report/README.en.md
	install -D -m 0644 METRICS.md $(DESTDIR)$(PREFIX)/share/doc/postfix-report/METRICS.md
	install -D -m 0644 LIMITATIONS.md $(DESTDIR)$(PREFIX)/share/doc/postfix-report/LIMITATIONS.md

# The unit refers to /usr/bin (RPM layout): rewrite it for $(PREFIX). With
# systemd < 245 (e.g. EL8) drop the directives it does not know.
SYSTEMD_VERSION ?= $(shell systemctl --version 2>/dev/null | awk 'NR==1 {print $$2+0}')
install-systemd:
	install -d $(DESTDIR)/etc/systemd/system
	sed -e 's|/usr/bin/$(BINARY)|$(PREFIX)/bin/$(BINARY)|' \
		$$(if [ -n "$(SYSTEMD_VERSION)" ] && [ "$(SYSTEMD_VERSION)" -lt 245 ]; then \
			echo "-e /^ProtectHostname=/d -e /^ProtectKernelLogs=/d -e /^ProtectClock=/d -e /^IPAddressDeny=/d"; fi) \
		deploy/systemd/postfix-report.service > $(DESTDIR)/etc/systemd/system/postfix-report.service
	chmod 0644 $(DESTDIR)/etc/systemd/system/postfix-report.service
	install -D -m 0644 deploy/systemd/postfix-report.timer $(DESTDIR)/etc/systemd/system/postfix-report.timer

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	rm -f $(DESTDIR)$(PREFIX)/share/man/man1/postfix-report.1
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

# Debian/Ubuntu package. Log paths default to /var/log/mail.log* (root:adm
# 0640, readable through the unit's SupplementaryGroups=adm).
deb:
	@command -v dpkg-deb >/dev/null || { echo "dpkg-deb not found"; exit 1; }
	@test -n "$(RPM_VERSION)" || { echo "Version not found in $(RPM_SPEC)"; exit 1; }
	rm -rf dist/deb/$(DEB_ARCH) && mkdir -p $(DEB_ROOT)/DEBIAN
	GOOS=linux GOARCH=$(DEB_ARCH) $(GO) build -trimpath -ldflags '-s -w -X main.version=$(DEB_VERSION)' -o $(DEB_ROOT)/usr/bin/$(BINARY) ./cmd/postfix-report
	install -D -m 0644 deploy/systemd/postfix-report.service $(DEB_ROOT)/usr/lib/systemd/system/postfix-report.service
	install -D -m 0644 deploy/systemd/postfix-report.timer $(DEB_ROOT)/usr/lib/systemd/system/postfix-report.timer
	install -d -m 0755 $(DEB_ROOT)/etc/postfix-report $(DEB_ROOT)/usr/share/man/man1 $(DEB_DOC)/examples
	install -D -m 0644 packaging/deb/lintian-overrides $(DEB_ROOT)/usr/share/lintian/overrides/$(BINARY)
	sed -e 's|/var/log/maillog-\*|/var/log/mail.log.*|' -e 's|/var/log/maillog$$|/var/log/mail.log|' config.example.yaml > $(DEB_ROOT)/etc/postfix-report/config.yaml
	chmod 0640 $(DEB_ROOT)/etc/postfix-report/config.yaml
	install -d -m 0750 $(DEB_ROOT)/var/lib/postfix-report
	gzip -9n -c docs/postfix-report.1 > $(DEB_ROOT)/usr/share/man/man1/postfix-report.1.gz
	install -m 0644 packaging/deb/copyright $(DEB_ROOT)/etc/postfix-report/config.yaml $(DEB_DOC)/
	mv $(DEB_DOC)/config.yaml $(DEB_DOC)/config.example.yaml
	for f in README.md README.en.md METRICS.md LIMITATIONS.md LOG_ANALYSIS.md VALIDATION.md; do gzip -9n -c $$f > $(DEB_DOC)/$$f.gz; done
	install -m 0644 deploy/apache/postfix-report.conf $(DEB_DOC)/examples/apache-postfix-report.conf
	install -m 0644 deploy/nginx/postfix-report.conf $(DEB_DOC)/examples/nginx-postfix-report.conf
	install -m 0644 deploy/cron/postfix-report $(DEB_DOC)/examples/cron-postfix-report
	printf '%s (%s) unstable; urgency=medium\n\n  * Upstream release %s (see the GitHub release notes).\n\n -- Francesco Defilippo <francesco@defilippo.org>  %s\n' \
		$(BINARY) $(DEB_VERSION) $(RPM_VERSION) '$(DEB_DATE)' | gzip -9n > $(DEB_DOC)/changelog.Debian.gz
	chmod 0644 $(DEB_DOC)/*.gz $(DEB_DOC)/copyright $(DEB_DOC)/config.example.yaml
	install -m 0755 packaging/deb/postinst packaging/deb/prerm packaging/deb/postrm $(DEB_ROOT)/DEBIAN/
	echo /etc/postfix-report/config.yaml > $(DEB_ROOT)/DEBIAN/conffiles
	sed -e 's/@VERSION@/$(DEB_VERSION)/' -e 's/@ARCH@/$(DEB_ARCH)/' \
		-e "s/@SIZE@/$$(du -sk --exclude=DEBIAN $(DEB_ROOT) | cut -f1)/" packaging/deb/control.in > $(DEB_ROOT)/DEBIAN/control
	chmod -R go-w $(DEB_ROOT)
	cd $(DEB_ROOT) && find . -type f ! -path './DEBIAN/*' -printf '%P\0' | sort -z | xargs -0 md5sum > DEBIAN/md5sums
	SOURCE_DATE_EPOCH=$$(git log -1 --format=%ct 2>/dev/null || date +%s) dpkg-deb --root-owner-group -Zxz --build $(DEB_ROOT) $(DEB_FILE)
	@echo $(DEB_FILE)

# Used by the release workflow: the tag (TAG=v0.1) must match the version in
# the spec file and in the man page.
release-check:
	@test -n "$(TAG)" || { echo "usage: make release-check TAG=v<version>"; exit 1; }
	@test "$(TAG)" = "v$(RPM_VERSION)" || { echo "tag $(TAG) does not match spec Version $(RPM_VERSION)"; exit 1; }
	@grep -q '"postfix-report $(RPM_VERSION)"' docs/postfix-report.1 || { echo "docs/postfix-report.1: .TH version is not $(RPM_VERSION)"; exit 1; }
	@echo "release $(TAG): ok"

clean:
	rm -rf bin dist

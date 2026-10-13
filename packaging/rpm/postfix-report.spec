# Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Pacchetto per Enterprise Linux 9/10 (RHEL, AlmaLinux, Rocky, Oracle Linux).
# Dal repository: make rpm (crea il tarball dei sorgenti con vendor/ e
# lancia rpmbuild). Il tarball contiene questo spec: rpmbuild -ta funziona.

# Binario Go statico, già privo di simboli (-s -w): niente pacchetto debuginfo.
%global debug_package %{nil}

Name:           postfix-report
Version:        0.1
Release:        1%{?dist}
Summary:        Statistiche Postfix con storico SQLite e report HTML statici

License:        GPL-3.0-or-later
URL:            https://github.com/fdefilippo/postfix-report
# make dist: sorgenti più vendor/ (la build non usa la rete).
Source0:        %{name}-%{version}.tar.gz

ExclusiveArch:  x86_64 aarch64 ppc64le s390x
BuildRequires:  golang >= 1.25
%if 0%{?rhel} && 0%{?rhel} < 9
BuildRequires:  systemd
%else
BuildRequires:  systemd-rpm-macros
%endif
Requires(pre):  shadow-utils
%{?systemd_requires}

%description
postfix-report legge i log di Postfix (anche ruotati o compressi), conserva lo
storico in un database SQLite e genera report HTML statici orari, giornalieri,
settimanali, mensili e annuali. L'importazione è incrementale e idempotente;
la copertura dei log è dichiarata (i dati assenti non sono mostrati come
zero). Un timer systemd aggiorna i report ogni ora.

%prep
%autosetup
# Esempi di configurazione per web server e cron, installati come documentazione.
mkdir -p examples
cp -p deploy/apache/%{name}.conf examples/apache-%{name}.conf
cp -p deploy/nginx/%{name}.conf examples/nginx-%{name}.conf
cp -p deploy/cron/%{name} examples/cron-%{name}

%build
export CGO_ENABLED=0
export GOFLAGS="-mod=vendor -trimpath"
export GOTOOLCHAIN=local
export GOPROXY=off
export GOCACHE=%{_builddir}/%{name}-%{version}/.gocache
go build -ldflags "-s -w -X main.version=%{version}-%{release}" \
    -o bin/%{name} ./cmd/postfix-report

%check
export CGO_ENABLED=0
export GOFLAGS="-mod=vendor"
export GOTOOLCHAIN=local
export GOPROXY=off
export GOCACHE=%{_builddir}/%{name}-%{version}/.gocache
export HOME=%{_builddir}/%{name}-%{version}/.home
mkdir -p "$HOME/tmp"
export TMPDIR="$HOME/tmp"
# -short: esclude i test sul log reale e quelli con il browser headless.
go test -short ./...

%install
install -D -m 0755 bin/%{name} %{buildroot}%{_bindir}/%{name}
install -D -m 0644 docs/%{name}.1 %{buildroot}%{_mandir}/man1/%{name}.1
install -D -m 0640 config.example.yaml %{buildroot}%{_sysconfdir}/%{name}/config.yaml
install -D -m 0644 deploy/systemd/%{name}.service %{buildroot}%{_unitdir}/%{name}.service
install -D -m 0644 deploy/systemd/%{name}.timer %{buildroot}%{_unitdir}/%{name}.timer
install -d -m 0750 %{buildroot}%{_sharedstatedir}/%{name}
install -d -m 0755 %{buildroot}%{_localstatedir}/www/html/%{name}

%pre
getent group %{name} >/dev/null || groupadd -r %{name}
getent passwd %{name} >/dev/null || \
    useradd -r -g %{name} -d %{_sharedstatedir}/%{name} -s /sbin/nologin \
    -c "postfix-report" %{name}
exit 0

%post
%systemd_post %{name}.service %{name}.timer

%preun
%systemd_preun %{name}.service %{name}.timer

%postun
%systemd_postun_with_restart %{name}.timer

%files
%license LICENSE
%doc README.md METRICS.md LIMITATIONS.md LOG_ANALYSIS.md VALIDATION.md config.example.yaml
%doc examples
%{_bindir}/%{name}
%{_mandir}/man1/%{name}.1*
%dir %attr(0755,root,root) %{_sysconfdir}/%{name}
%config(noreplace) %attr(0640,root,%{name}) %{_sysconfdir}/%{name}/config.yaml
%{_unitdir}/%{name}.service
%{_unitdir}/%{name}.timer
%dir %attr(0750,%{name},%{name}) %{_sharedstatedir}/%{name}
%dir %attr(0755,%{name},%{name}) %{_localstatedir}/www/html/%{name}

%changelog
* Tue Oct 13 2026 Francesco Defilippo <francesco@defilippo.org> - 0.1-1
- Primo pacchetto RPM.

# Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Pacchetto per Enterprise Linux 8/9/10 (RHEL, AlmaLinux, Rocky, Oracle Linux).
# Dal repository: make rpm (crea il tarball dei sorgenti con vendor/ e
# lancia rpmbuild). Il tarball contiene questo spec: rpmbuild -ta funziona.

# Binario Go statico, già privo di simboli (-s -w): niente pacchetto debuginfo.
%global debug_package %{nil}

Name:           postfix-report
Version:        0.5.0
Release:        1%{?dist}
Summary:        Statistiche Postfix con report HTML, PDF e ODT

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
zero). Un timer systemd aggiorna i report ogni ora. Il daemon web opzionale
serve i report e genera documenti PDF e ODT su richiesta dal browser; gli
stessi formati sono esportabili dalla riga di comando.

%prep
%autosetup
# Esempi di configurazione per web server e cron, installati come documentazione.
mkdir -p examples
cp -p deploy/apache/%{name}.conf examples/apache-%{name}.conf
cp -p deploy/nginx/%{name}.conf examples/nginx-%{name}.conf
cp -p deploy/nginx/%{name}-web.conf examples/nginx-%{name}-web.conf
cp -p deploy/apache/%{name}-web.conf examples/apache-%{name}-web.conf
cp -p docs/apache-httpd.conf docs/nginx.conf examples/
cp -p deploy/cron/%{name} examples/cron-%{name}

%build
export CGO_ENABLED=0
# The EL9/EL10 Go toolchain defaults to GOAMD64=v2/v3 (go.env): the binary
# would refuse to start on older CPUs and on VMs with a generic CPU model.
export GOAMD64=v1
export GOFLAGS="-mod=vendor -trimpath"
export GOTOOLCHAIN=local
export GOPROXY=off
export GOCACHE=%{_builddir}/%{name}-%{version}/.gocache
go build -ldflags "-s -w -X main.version=%{version}-%{release}" \
    -o bin/%{name} ./cmd/postfix-report

%check
%ifarch x86_64
go version -m bin/%{name} | grep -q 'GOAMD64=v1' || { echo "binary not built for GOAMD64=v1"; exit 1; }
%endif
export CGO_ENABLED=0
export GOAMD64=v1
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
install -D -m 0644 deploy/systemd/%{name}-web.service %{buildroot}%{_unitdir}/%{name}-web.service
%if 0%{?rhel} && 0%{?rhel} < 9
# systemd 239: no ProtectHostname/ProtectKernelLogs/ProtectClock, and no BPF
# firewall with cgroup v1 (PrivateNetwork=yes already isolates the service).
sed -i -e '/^ProtectHostname=/d' -e '/^ProtectKernelLogs=/d' -e '/^ProtectClock=/d' \
    -e '/^IPAddressDeny=/d' %{buildroot}%{_unitdir}/%{name}.service
sed -i -e '/^ProtectHostname=/d' -e '/^ProtectKernelLogs=/d' -e '/^ProtectClock=/d' \
    -e '/^IPAddressDeny=/d' -e '/^IPAddressAllow=/d' %{buildroot}%{_unitdir}/%{name}-web.service
%endif
install -d -m 0750 %{buildroot}%{_sharedstatedir}/%{name}
install -d -m 0755 %{buildroot}%{_localstatedir}/www/html/%{name}

install -D -m 0644 docs/PERFORMANCE.md %{buildroot}%{_docdir}/%{name}/docs/PERFORMANCE.md

%pre
getent group %{name} >/dev/null || groupadd -r %{name}
getent passwd %{name} >/dev/null || \
    useradd -r -g %{name} -d %{_sharedstatedir}/%{name} -s /sbin/nologin \
    -c "postfix-report" %{name}
exit 0

%post
# The web daemon is opt-in: do not apply a distribution enable-all preset.
# The existing macro still reloads systemd after installing all three units.
%systemd_post %{name}.service %{name}.timer

%preun
%systemd_preun %{name}.service %{name}.timer %{name}-web.service

%postun
%systemd_postun_with_restart %{name}.timer %{name}-web.service

%files
%license LICENSE docs/licenses
%doc README.md README.it.md METRICS.md LIMITATIONS.md LOG_ANALYSIS.md VALIDATION.md EXPORTS.md config.example.yaml
%doc examples
%{_docdir}/%{name}/docs/PERFORMANCE.md
%{_bindir}/%{name}
%{_mandir}/man1/%{name}.1*
%dir %attr(0755,root,root) %{_sysconfdir}/%{name}
%config(noreplace) %attr(0640,root,%{name}) %{_sysconfdir}/%{name}/config.yaml
%{_unitdir}/%{name}.service
%{_unitdir}/%{name}.timer
%{_unitdir}/%{name}-web.service
%dir %attr(0750,%{name},%{name}) %{_sharedstatedir}/%{name}
%dir %attr(0755,%{name},%{name}) %{_localstatedir}/www/html/%{name}

%changelog
* Tue Nov 10 2026 Francesco Defilippo <francesco@defilippo.org> - 0.5.0-1
- Ritiro e ripristino dei report disabilitati con recupero delle pubblicazioni interrotte.
- Finestra notifiche configurabile e replay storico recuperabile dopo errori parziali.
- Percorsi SQLite letterali, verifica integrita e limite esplicito delle righe di log.
- Error handling verificato, CI push/PR e benchmark riproducibili senza nuovi indici.

* Wed Nov 04 2026 Francesco Defilippo <francesco@defilippo.org> - 0.4.1-1
- Esempi Apache HTTPD e Nginx documentati in docs/ per export PDF/ODT via HTTPS.
- Esempi inclusi in RPM, DEB e installazione manuale, con link nelle guide.
- Sezione licenza mantenuta alla fine dei README inglese e italiano.

* Wed Nov 04 2026 Francesco Defilippo <francesco@defilippo.org> - 0.4-1
- Archivio unico PDF/ODT con indice navigabile, selezione date e granularità.
- Pannello con anteprima conteggi in cima all'indice e CLI export --all.
- Snapshot coerente per tutti i periodi e limiti espliciti senza troncamenti.
- Header su una riga: server a sinistra, periodo al centro, data UTC a destra.

* Wed Nov 04 2026 Francesco Defilippo <francesco@defilippo.org> - 0.3-1
- Esportazione PDF e ODT per giorno, settimana ISO, mese e anno, anche da CLI.
- Daemon serve opzionale, export on demand dal browser e job privati limitati.
- Unità systemd web separata, proxy autenticati, documentazione e licenze.
- Letture SQLite read-only consistenti con il writer e aggiornamenti privacy.
- Correzioni di integrità checkpoint, correlazione, copertura e verifica dati.

* Sun Oct 25 2026 Francesco Defilippo <francesco@defilippo.org> - 0.2-1
- Report anche in inglese (output.language: en); l'italiano resta predefinito.
- README.md in inglese, versione italiana in README.it.md.
- CSS e JS referenziati con l'hash del contenuto: dopo un aggiornamento il
  browser non usa copie in cache.
- Sfondo dei fine settimana più marcato nei grafici per giorno.

* Wed Oct 14 2026 Francesco Defilippo <francesco@defilippo.org> - 0.1.5-1
- Navigazione: voci Giornaliero, Settimanale, Mensile, Annuale.

* Wed Oct 14 2026 Francesco Defilippo <francesco@defilippo.org> - 0.1.4-1
- Report: barra di navigazione sempre visibile, titolo mostrato una volta,
  tutti i periodi sempre elencati nella navigazione.
- Grafici per giorno: sabato e domenica con sfondo distinto.

* Tue Oct 13 2026 Francesco Defilippo <francesco@defilippo.org> - 0.1.3-1
- EL8: unità systemd senza le direttive sconosciute a systemd 239
  (ProtectHostname, ProtectKernelLogs, ProtectClock, IPAddressDeny).
- README in inglese (README.en.md).

* Tue Oct 13 2026 Francesco Defilippo <francesco@defilippo.org> - 0.1.2-1
- Report: footer con copyright, repository e licenza.
- Database dei fusi orari incorporato nel binario (sistemi senza tzdata).
- Pacchetti .deb per Debian e Ubuntu (amd64, arm64).

* Tue Oct 13 2026 Francesco Defilippo <francesco@defilippo.org> - 0.1.1-1
- Binario compilato per x86-64 di base (GOAMD64=v1): la 0.1 non partiva su
  CPU senza x86-64-v2.
- Pacchetto anche per Enterprise Linux 8.

* Tue Oct 13 2026 Francesco Defilippo <francesco@defilippo.org> - 0.1-1
- Primo pacchetto RPM.

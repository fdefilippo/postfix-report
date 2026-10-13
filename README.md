# postfix-report

Analisi dei log di Postfix con storico in SQLite e **report HTML statici**
(giornalieri, settimanali ISO 8601, mensili, annuali), aggiornati in modo
incrementale da un timer systemd. Non servono servizi esterni, Internet o
server applicativi: i report si consultano da qualunque web server statico
oppure direttamente via `file://`.

* **Statistiche verificabili**: messaggi, destinatari, tentativi, consegne,
  differimenti, fallimenti definitivi, rifiuti SMTP, bounce, volumi, ritardi
  con percentili esatti, TLS e SASL. Le definizioni sono in [METRICS.md](METRICS.md),
  il confronto con conteggi indipendenti sul log reale in [VALIDATION.md](VALIDATION.md).
* **Correlazione corretta**: Queue ID riutilizzati, messaggi su più giorni e su
  più file, destinatari multipli contati una volta, differimenti seguiti da
  successo, reiniezione dei content filter riconosciuta (un messaggio filtrato
  non diventa due messaggi).
* **Dati mancanti ≠ zero**: ogni ora, giorno e periodo riporta la propria
  copertura. Le ore senza log sono mostrate come assenti, non come traffico zero.
* **Import idempotente**: le sorgenti sono riconosciute dal contenuto. Rotazione,
  file `.gz`, riletture, sovrapposizioni e interruzioni non duplicano i dati.
* **Rigenerazione selettiva**: si riscrivono solo le pagine i cui dati sono
  cambiati, con sostituzione atomica dei file.
* **Pagine a schede**: gli indicatori del periodo restano in alto; il resto è
  diviso in schede (traffico, confronto, tempi di consegna, mittenti e
  destinatari, rifiuti ed errori, sicurezza, copertura). La scheda scelta resta
  attiva passando a un altro report ed è indicata nell'URL (`#rifiuti-ed-errori`).
  Senza JavaScript e in stampa le sezioni compaiono tutte, una sotto l'altra.

![Report giornaliero, scheda Traffico (dati sintetici)](docs/screenshots/giornaliero.png)

<details>
<summary>Altre schermate (dati sintetici)</summary>

![Report mensile, scheda Rifiuti ed errori](docs/screenshots/mensile-rifiuti.png)

![Panoramica con l'archivio](docs/screenshots/panoramica-archivio.png)

</details>

Analisi dei formati osservati: [LOG_ANALYSIS.md](LOG_ANALYSIS.md) · Limiti noti:
[LIMITATIONS.md](LIMITATIONS.md) · Report dimostrativi (dati sintetici):
[`demo/index.html`](demo/index.html).

## Architettura

```text
maillog(.gz) ─► parser streaming ─► eventi normalizzati ─► correlazione Queue ID
                (internal/logparse)  (internal/ingest)      (internal/correlate)
                                                                   │
  report HTML ◄── generatore ◄── interrogazioni ◄── aggregati ◄── SQLite
  (output/)      (internal/report) (internal/query) (internal/aggregate) (internal/store)
```

| Package | Responsabilità |
|---|---|
| `internal/logparse` | Parsing sintattico delle righe syslog/Postfix, anno e ora legale |
| `internal/ingest` | Sorgenti (fingerprint), checkpoint, gzip, deduplicazione, copertura |
| `internal/correlate` | Istanze di coda, destinatari, tentativi, reiniezioni, rifiuti |
| `internal/aggregate` | Metriche orarie e classifiche giornaliere, ricalcolabili |
| `internal/query` | Totali, serie, copertura, percentili esatti |
| `internal/report` | Pagine HTML, asset CSS/JS locali, scrittura atomica |
| `internal/verify` | Integrità DB, invarianti, aggregati, link dei report |
| `internal/app` | Orchestrazione, lock, invalidazione delle pagine |

Il database è la fonte dei report: i log possono essere ruotati via senza
perdere lo storico. Lo schema è versionato (`internal/store/migrations`).

## Compilazione

Serve Go 1.25 o successivo. Il binario è statico (senza cgo, con il driver
SQLite in Go puro): si compila ovunque e gira su qualsiasi Linux della stessa
architettura, Enterprise Linux 8/9/10 compresi.

```bash
make build        # bin/postfix-report
make check        # gofmt, go vet, test
make bench        # benchmark del parser e della pipeline
```

## Installazione con RPM (Enterprise Linux 8/9/10)

Gli RPM già compilati per EL8, EL9 ed EL10 sono allegati alle
[release su GitHub](https://github.com/fdefilippo/postfix-report/releases),
con SRPM, sorgenti e `SHA256SUMS`. Per compilarli in proprio:

Su una macchina EL con `golang` (≥ 1.25) e `rpm-build` installati:

```bash
sudo dnf install golang rpm-build systemd-rpm-macros git make
make rpm          # dist/rpmbuild/RPMS/x86_64/postfix-report-0.1-1.el9.x86_64.rpm
sudo dnf install dist/rpmbuild/RPMS/x86_64/postfix-report-*.rpm
```

`make rpm` crea prima `dist/postfix-report-<versione>.tar.gz` (sorgenti più
`vendor/`, quindi `rpmbuild` non usa la rete) e poi lancia `rpmbuild -ba`.
`make srpm` produce solo l'SRPM, utile con `mock`. Per pubblicare una
release si aggiornano `Version` e `%changelog` nello spec e la versione in
`docs/postfix-report.1`, poi si crea e si invia il tag (`git tag -a v0.2 &&
git push origin v0.2`): il workflow `.github/workflows/release.yml` controlla
le versioni (`make release-check`), compila gli RPM e crea la release. Il
workflow parte solo sui tag. La versione è quella
indicata nello spec (`packaging/rpm/postfix-report.spec`). Il binario è
statico: un RPM compilato su EL9 si installa anche su EL10.

Il pacchetto installa:

| Percorso | Contenuto |
|---|---|
| `/usr/bin/postfix-report` | binario |
| `/etc/postfix-report/config.yaml` | configurazione (`root:postfix-report 0640`, non sovrascritta negli aggiornamenti) |
| `/usr/lib/systemd/system/postfix-report.{service,timer}` | unità systemd (timer non abilitato) |
| `/var/lib/postfix-report/` | database (`0750`) |
| `/var/www/html/postfix-report/` | report HTML (`0755`) |
| `/usr/share/doc/postfix-report/examples/` | configurazioni di esempio per Apache, Nginx e cron |

Crea anche l'utente di sistema `postfix-report`. Dopo l'installazione restano
da fare: i permessi di lettura dei log (sotto), il controllo della
configurazione e l'abilitazione del timer:

```bash
sudo postfix-report -config /etc/postfix-report/config.yaml check-config
sudo runuser -u postfix-report -- postfix-report -config /etc/postfix-report/config.yaml update
sudo systemctl enable --now postfix-report.timer
```

## Installazione con .deb (Debian, Ubuntu)

I pacchetti `.deb` per amd64 e arm64 sono allegati alle stesse release. Il
binario è statico (con il database dei fusi orari incorporato), quindi lo
stesso pacchetto va su Debian 12/13 e Ubuntu 22.04/24.04 e successive:

```bash
sudo apt install ./postfix-report_<versione>-1_amd64.deb
```

Per costruirlo in proprio basta Go ≥ 1.25 e `dpkg-deb`:
`make deb` (oppure `make deb DEB_ARCH=arm64`) produce
`dist/postfix-report_<versione>-1_<arch>.deb`. Il Go di Debian è troppo
vecchio per questo modulo: il pacchetto si assembla attorno al binario
compilato con il Go ufficiale, senza dipendenze di sistema.

Rispetto all'RPM cambiano solo questi dettagli:

* **Log**: in `/etc/postfix-report/config.yaml` i percorsi predefiniti sono
  `/var/log/mail.log` e `/var/log/mail.log.*`. Sono file `root:adm 0640`,
  letti dal servizio tramite `SupplementaryGroups=adm`: non servono ACL. Per
  altri percorsi basta modificare `input.paths`.
* **Directory dei report**: `/var/www/html/postfix-report` è creata da
  `postinst` (la policy Debian non permette di includerla nel pacchetto).
* **Timer**: non è abilitato all'installazione. Se l'amministratore lo
  abilita, la scelta resta valida negli aggiornamenti; `apt purge` la azzera.
* **Rimozione**: `apt purge` rimuove la configurazione, ma lascia database,
  report e utente di sistema.

## Installazione manuale (esempio per Enterprise Linux)

```bash
# 1. Binario e documentazione
sudo make install                       # /usr/local/bin/postfix-report

# 2. Utente di servizio senza login (il programma non deve girare come root)
sudo useradd --system --home-dir /var/lib/postfix-report --shell /sbin/nologin postfix-report

# 3. Directory: database privato, HTML leggibile dal web server
sudo install -d -m 0750 -o postfix-report -g postfix-report /var/lib/postfix-report
sudo install -d -m 0755 -o postfix-report -g postfix-report /var/www/html/postfix-report
sudo install -d -m 0755 /etc/postfix-report
sudo install -m 0640 -o root -g postfix-report config.example.yaml /etc/postfix-report/config.yaml

# 4. SELinux (EL): ripristina i contesti della directory pubblicata
sudo restorecon -Rv /var/www/html/postfix-report
```

### Permessi di lettura dei log

Il servizio legge i log con l'utente `postfix-report`.

* **Debian/Ubuntu**: `/var/log/mail.log*` sono `root:adm 0640`.
  L'unità systemd aggiunge già il gruppo `adm` (`SupplementaryGroups=adm`).
  In configurazione usare `paths: [/var/log/mail.log, /var/log/mail.log.*]`.
* **Enterprise Linux**: `/var/log/maillog` è `root:root 0600`. Due alternative:
  1. **rsyslog** (consigliata): far creare il file con un gruppo dedicato, in
     `/etc/rsyslog.d/maillog.conf`:
     ```
     mail.* action(type="omfile" file="/var/log/maillog" fileGroup="postfix-report" fileCreateMode="0640")
     ```
     Va rimossa la riga `mail.*` predefinita da `/etc/rsyslog.conf`. Poi:
     `systemctl restart rsyslog`. I file ruotati mantengono il gruppo.
  2. **ACL**: `setfacl -m u:postfix-report:r /var/log/maillog`. L'ACL va
     riapplicata dopo ogni rotazione con un `postrotate` in
     `/etc/logrotate.d/rsyslog`. Se la rotazione comprime i file, verificare
     che i `.gz` restino leggibili.

  Verifica: `runuser -u postfix-report -- head -1 /var/log/maillog`.

### Permessi di scrittura

* Il database (`/var/lib/postfix-report/report.db`) è creato con permessi
  `0640` in una directory `0750`: contiene indirizzi e IP e **non deve stare
  sotto la directory web** (la configurazione lo rifiuta).
* La directory HTML deve essere scrivibile dall'utente `postfix-report` e
  leggibile dal web server. Le pagine sono create con permessi `0644`.

## Configurazione

Vedi [`config.example.yaml`](config.example.yaml), commentato. Lo schema è
validato in modo stretto (chiavi sconosciute, durate, fuso, anno, percorsi):

```bash
postfix-report -config /etc/postfix-report/config.yaml check-config
```

Punti da adattare al server:

* `input.paths`: file e glob dei log, compresi i ruotati e i `.gz`.
* `input.timezone`: il fuso dei timestamp syslog.
* `correlation.reinject_uids` e `correlation.filter_transports`: indicano se,
  e come, un content filter reinietta i messaggi (vedi LOG_ANALYSIS.md §4.1).
  Dopo il primo import, `verify` segnala reiniezioni orfane o passaggi al filtro
  senza reiniezione, che indicano una configurazione da correggere.

## Uso

```bash
postfix-report import --file /var/log/maillog
postfix-report import --directory /var/log/mail
postfix-report import --file /var/log/maillog.1.gz

postfix-report generate                  # solo le pagine invalidate
postfix-report generate --all
postfix-report generate --date 2026-10-12
postfix-report generate --week 2026-W42
postfix-report generate --month 2026-10
postfix-report generate --year 2026

postfix-report update     # importa input.paths, ricalcola, rigenera il necessario
postfix-report rebuild    # ricorrela tutti gli eventi memorizzati e rigenera tutto
postfix-report verify     # integrità, coerenza, link dei report
postfix-report stats --from 2026-10-11 --to 2026-10-13 [--json]
```

Il file di configurazione si indica con `-config FILE` (default
`/etc/postfix-report/config.yaml`, oppure `POSTFIX_REPORT_CONFIG`).

| Codice di uscita | Significato |
|---|---|
| 0 | eseguito correttamente |
| 1 | errore |
| 2 | uso o configurazione non validi |
| 3 | un'altra esecuzione è in corso (lock): nessuna azione |
| 4 | `verify` ha trovato incoerenze |
| 5 | import parziale: alcuni file non leggibili, gli altri importati |

### Cosa fa `update`

1. Espande `input.paths` e ordina i file dal più vecchio (primo timestamp).
2. Riconosce ogni file dal contenuto (hash dei primi 4 KiB decompressi). Un
   file già letto per intero viene saltato; un file cresciuto o ruotato
   riprende dall'offset salvato.
3. Salva eventi e checkpoint nella stessa transazione: un'interruzione non
   lascia stati intermedi e l'esecuzione successiva riprende dal punto giusto.
4. Correla i nuovi eventi partendo dallo stato dei messaggi ancora in coda.
   Se arrivano eventi più vecchi di quelli già correlati, l'host viene
   ricorrelato da zero.
5. Ricalcola gli aggregati dei soli giorni toccati e invalida le pagine che ne
   dipendono: giorno, settimana, mese, anno, i periodi adiacenti (link e
   confronti) e l'indice.
6. Rigenera le pagine invalidate. Una pagina è riscritta solo se il suo
   contenuto è cambiato, scrivendo su un file temporaneo e rinominandolo.
   Dopo un aggiornamento del programma o una modifica alle opzioni dei report
   (`output`, `reports`, `privacy`) la prima esecuzione rigenera tutte le
   pagine, così l'archivio ha un aspetto uniforme.

Un lock (`<database>.lock`, `flock`) impedisce esecuzioni concorrenti.

## Esecuzione automatica (systemd)

```bash
sudo make install-systemd        # non serve se si è installato l'RPM
sudo systemctl daemon-reload
sudo systemctl enable --now postfix-report.timer
systemctl list-timers postfix-report.timer
journalctl -u postfix-report.service
```

Il timer esegue `update` ogni ora. Per cambiare frequenza:

```bash
sudo systemctl edit postfix-report.timer
# [Timer]
# OnCalendar=
# OnCalendar=*:0/15
```

L'unità gira come `postfix-report`, senza rete (`PrivateNetwork=yes`) e con il
filesystem in sola lettura salvo database e directory HTML. In alternativa al
timer si può usare il file cron `deploy/cron/postfix-report`.

## Pubblicazione con Apache o Nginx

I report contengono informazioni sul traffico (domini, IP, utenti SASL,
motivi di rifiuto): **vanno protetti** con autenticazione e, possibilmente,
con restrizioni di rete. Esempi pronti:

* Apache: [`deploy/apache/postfix-report.conf`](deploy/apache/postfix-report.conf)
* Nginx: [`deploy/nginx/postfix-report.conf`](deploy/nginx/postfix-report.conf)

Entrambi abilitano Basic Auth (da usare solo su HTTPS), negano i file nascosti
e impostano una Content-Security-Policy compatibile con i report: script solo
locali, nessuna connessione verso l'esterno.

Le pagine usano solo percorsi relativi: la directory si può pubblicare sotto
qualsiasi prefisso URL, copiare altrove o aprire con `file://`. I dati dei
grafici sono incorporati in ogni pagina: non ci sono `fetch()` né CDN.

## Sicurezza e privacy

* Tutti i dati dei log sono soggetti a escaping HTML (`html/template`). I dati
  dei grafici sono JSON incorporato in `<script type="application/json">`.
* `privacy.anonymize_email` pseudonimizza la parte locale degli indirizzi e gli
  utenti SASL con HMAC. La chiave è nel database, mai nei report.
  `privacy.anonymize_ip` tronca gli IP a /24 o /48. Entrambe le opzioni sono
  disattivate di default.
* I motivi di rifiuto e differimento sono normalizzati (indirizzi → `<addr>`,
  IP → `[ip]`) per raggrupparli. Il testo originale resta solo nel database.
* La directory web contiene solo HTML, CSS e JS. Database e log non vi sono
  mai scritti. `verify` segnala eventuali file estranei.
* Gli oggetti dei messaggi (righe `warning: header Subject:`) non vengono
  memorizzati.

## Test e validazione

```bash
make test        # unit, integrazione, end-to-end
make validate    # confronto con il ricalcolo indipendente sul log di riferimento
make demo        # rigenera demo/ da dati sintetici
```

* I test di regressione sul log reale si attivano se esiste `samples/maillog`
  (oppure `./maillog`, o `POSTFIX_REPORT_REAL_LOG`). Il log non è distribuito:
  i test conservano solo conteggi aggregati.
* I test sui report aprono le pagine con un server HTTP statico sotto un
  prefisso URL e, se Chromium è installato, via `file://` in modalità headless.
* `cmd/synthlog` genera log sintetici realistici (domini `example.*`, IP di
  documentazione) per demo, benchmark e test.

## Prestazioni

Su un Intel N150 (4 core, basso consumo): parser circa 0,5 µs per riga;
pipeline completa (import, correlazione, aggregazione) circa 24.000 righe/s.
Il log di riferimento (254.884 righe) si importa in circa 10 s. Il database
occupa circa 30 MB per 150.000 righe di log al giorno (vedi LIMITATIONS.md).

## Licenza

Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>

Questo programma è software libero: puoi ridistribuirlo e/o modificarlo nei
termini della GNU General Public License pubblicata dalla Free Software
Foundation, versione 3 o (a tua scelta) qualsiasi versione successiva.
Vedi [LICENSE](LICENSE). SPDX: `GPL-3.0-or-later`.

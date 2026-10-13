# LOG_ANALYSIS.md — Analisi del maillog di riferimento

Documento della Fase 1. Descrive i formati **effettivamente osservati** nel file
di riferimento (`maillog`, 39 MB, 254.884 righe) e le regole di interpretazione
adottate da `postfix-report`. Gli esempi sono **anonimizzati**: domini sostituiti
con `example.org`/`example.net`, indirizzi IP con blocchi di documentazione
(RFC 5737 `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`), utenti con nomi
fittizi, Queue ID e message-id riscritti. Gli oggetti dei messaggi non sono
riportati.

I conteggi sono stati ottenuti con `rg`, `awk`, `sort | uniq -c` direttamente
sul file, in modo indipendente dal programma.

## 1. Formato syslog e struttura temporale

```
Oct 11 03:25:42 mx1 postfix/smtpd[1076730]: DA7E57F733: client=unknown[192.0.2.14]
^^^^^^^^^^^^^^^ ^^^ ^^^^^^^ ^^^^^ ^^^^^^^   ^^^^^^^^^^  ^^^^^^^^^^^^^^^^^^^^^^^^^^^^
timestamp BSD   host syslog  servizio PID  Queue ID     payload
(senza anno,         name
 senza fuso)
```

| Caratteristica | Osservato |
|---|---|
| Formato timestamp | BSD syslog `Mmm dd HH:MM:SS` (RFC 3164), **senza anno e senza fuso orario**, risoluzione 1 s |
| Periodo | da `Oct 11 03:22:02` a `Oct 13 11:32:40` |
| Righe per giorno | 11/10: 25.687 · 12/10: 157.641 · 13/10: 71.556 |
| Host | uno solo (nome omesso) |
| Istanze Postfix | una sola, syslog name `postfix` (nessuna istanza `postfix-xxx`) |
| Ordinamento | timestamp monotòni non decrescenti (0 salti all'indietro) |
| Buchi temporali | nessun intervallo senza righe superiore a 5 minuti |
| Righe identiche | 14.333 righe sono duplicati esatti **legittimi**, ad es. `discarding EHLO keywords: CHUNKING` ripetuto due volte per sessione, `connect from` di sessioni diverse nello stesso secondo, 3 coppie di `NOQUEUE: reject` identiche nello stesso secondo |
| Fuso orario | i message-id generati localmente (`20261011012202.<QID>@…` per una riga delle `03:22:02`) mostrano un offset di +2 h: ora locale Europe/Rome (CEST) |
| Anno | deducibile solo dal contesto (message-id `2026…`, mtime del file) |

Conseguenze per l'implementazione:

* L'anno va **dedotto** (mtime del file e rilevamento del passaggio dicembre→gennaio)
  oppure fornito in configurazione. Sono supportati anche i timestamp RFC 3339
  (`2026-10-11T03:22:02.123456+02:00`), nei quali anno e offset sono espliciti.
* Il fuso orario si configura (`input.timezone`). Il ritorno all'ora solare
  (un'ora ripetuta) si risolve con il vincolo di monotonia dei timestamp.
* La deduplicazione **non** può basarsi sul contenuto della riga: si basa
  sull'identità della sorgente e sull'offset (vedi §11).

## 2. Processi Postfix presenti

| Processo | Righe | Ruolo osservato |
|---|---:|---|
| `postfix/smtpd` | 89.809 | sessioni SMTP in ingresso, SASL, rifiuti `NOQUEUE` |
| `postfix/qmgr` | 58.042 | ingresso in coda attiva, rimozione, scadenza |
| `postfix/cleanup` | 56.710 | message-id, warning `header Subject`, `milter-reject` |
| `postfix/smtp` | 17.479 | consegne SMTP remote |
| `postfix/pipe` | 13.687 | consegna al **content filter** `relay=filter` |
| `postfix/pickup` | 12.892 | iniezione locale da maildrop (`uid=`) |
| `postfix/lmtp` | 2.479 | consegna locale a Dovecot LMTP |
| `postfix/bounce` | 1.895 | generazione notifiche DSN |
| `postfix/anvil` | 1.038 | statistiche di rate (informative) |
| `postfix/error` | 588 | differimenti "delivery temporarily suspended" |
| `postfix/scache` | 243 | statistiche connection cache (informative) |
| `postfix/postfix-script` | 11 | start/stop, warning permessi |
| `postfix/master` | 6 | avvio/terminazione daemon |
| `postfix/verify` | 5 | pulizia cache di verifica indirizzi |

Non sono presenti: `postscreen`, `local`, `virtual`, `discard`, `smtp` con log TLS,
`opendkim`, `opendmarc`, `amavis`, `spamd`, `rspamd`, `clamav`, `policyd`.

## 3. Queue ID

* Formato **breve**: 10 caratteri esadecimali maiuscoli (`DA7E57F733`).
  Nessun Queue ID in formato lungo (`enable_long_queue_ids`). Il parser
  supporta comunque anche il formato lungo (`4Y2bQk1qzKz9vS`, 12+ caratteri
  alfanumerici).
* 175.649 righe hanno un Queue ID. Le altre 79.235 non lo hanno (§9).
* **Riutilizzo del Queue ID osservato nello stesso file**. Esempio (anonimizzato):

  ```
  Oct 12 09:35:20 mx1 postfix/pickup[…]: 13C237F646: uid=97 from=<noreply@example.org>
  …
  Oct 12 09:35:45 mx1 postfix/qmgr[…]: 13C237F646: removed
  Oct 12 11:38:23 mx1 postfix/smtpd[…]: 13C237F646: client=unknown[192.0.2.31]
  …
  Oct 12 11:38:23 mx1 postfix/qmgr[…]: 13C237F646: removed
  ```

  Il Queue ID breve deriva da inode e microsecondi, quindi può ripetersi dopo la
  rimozione. La correlazione apre perciò una **nuova istanza di coda** quando
  dopo un `removed` compare un evento iniziale (`client=`, `uid=`, `message-id=`),
  oppure quando un'istanza già iniziata riceve un secondo evento iniziale.
* Riavvii di Postfix osservati: ogni giorno alle 00:00 (stop + start) e il
  13/10 alle 08:14:57 (solo start, PID bassi: probabile riavvio dell'host). I
  messaggi in coda sopravvivono ai riavvii e mantengono il Queue ID.

## 4. Ciclo di vita e accettazione

### 4.1 Messaggio ricevuto via SMTP e passato al content filter

```
smtpd   DA7E57F733: client=unknown[192.0.2.14]
cleanup DA7E57F733: message-id=<20261011012542.A900C580828@host.example.org>
qmgr    DA7E57F733: from=<monitor@example.org>, size=8585, nrcpt=1 (queue active)
pickup  206487F6D9: uid=97 from=<monitor@example.org>              <- reiniezione
cleanup 206487F6D9: message-id=<20261011012542.A900C580828@host.example.org>
qmgr    206487F6D9: from=<monitor@example.org>, size=8674, nrcpt=1 (queue active)
pipe    DA7E57F733: to=<ops@example.org>, relay=filter, delay=0.26, delays=0.21/0.02/0/0.03, dsn=2.0.0, status=sent (delivered via filter service)
qmgr    DA7E57F733: removed
smtp    206487F6D9: to=<ops@example.org>, relay=mx.example.net[198.51.100.27]:25, delay=1.3, delays=0.01/0/0.35/0.93, dsn=2.6.0, status=sent (250 2.6.0 <…> Queued mail for delivery)
qmgr    206487F6D9: removed
```

**Rilievo fondamentale.** Il server usa un *content filter* implementato come
trasporto `pipe` chiamato `filter`. Il filtro reinietta il messaggio con
`sendmail`, che arriva tramite `pickup` con `uid=97` e **un nuovo Queue ID**,
lo stesso message-id, lo stesso mittente e lo stesso `nrcpt`, e una dimensione
leggermente maggiore (+80–100 byte di intestazioni aggiunte).

Misure sul file:

| Misura | Valore |
|---|---:|
| righe `pipe … relay=filter … status=sent (delivered via filter service)` | 13.687 |
| Queue ID distinti consegnati al filtro | 12.880 |
| righe `pickup … uid=97` | 12.889 |
| righe `pickup … uid=0` (root, cron) | 3 |
| messaggi `smtpd` consegnati al filtro / messaggi `smtpd` | 12.880 / 13.911 |

Un messaggio con N destinatari genera **N righe `pipe`** (stesso PID, stessi
`delays`) ma **una sola** reiniezione con `nrcpt=N` (osservati 316 messaggi con
2 destinatari, 21 con 4, 21 con 5, 12 con 22, …).

Conseguenze per le statistiche:

* La riga `pipe relay=filter status=sent` **non è una consegna**. È il
  passaggio interno al filtro (*handoff*).
* Il messaggio reiniettato **non è un nuovo messaggio accettato**. È la
  seconda istanza di coda dello stesso messaggio logico.
* Senza questa correlazione i messaggi accettati risulterebbero quasi
  raddoppiati (13.911 + 12.889) e le "consegne" includerebbero 13.687
  passaggi interni.
* La correlazione originale → reiniezione usa: stesso host, `uid` di
  reiniezione configurato (default `[97]`, dedotto dal log), stesso mittente,
  stesso message-id (se presente), `nrcpt` uguale e distanza temporale entro
  una finestra configurabile (default 120 s). Il message-id da solo non basta:
  alcuni applicativi riusano lo stesso message-id per centinaia di messaggi
  (fino a 395 messaggi distinti con lo stesso message-id), e alcuni messaggi
  hanno `message-id=<>`. Per questo l'abbinamento sceglie il candidato non
  ancora abbinato più vicino nel tempo.
* La reiniezione compare **prima** della riga `pipe … status=sent` (il filtro
  termina dopo aver chiamato `sendmail`). L'abbinamento quindi considera
  anche i messaggi la cui consegna al filtro è ancora in corso.

### 4.2 Messaggio originato localmente

```
pickup  7DB3E7F733: uid=0 from=<root>
cleanup 7DB3E7F733: message-id=<20261011012202.7DB3E7F733@mx1.example.org>
qmgr    7DB3E7F733: from=<root@example.org>, size=518, nrcpt=1 (queue active)
smtp    7DB3E7F733: to=<root@example.org>, orig_to=<root>, relay=…, status=sent (…)
qmgr    7DB3E7F733: removed
```

`pickup` con uid diverso da quello del filtro (qui `uid=0`, 3 casi) indica
un messaggio originato localmente. Questi messaggi non passano dal filtro.

### 4.3 Accettazione

Il messaggio è considerato **accettato** quando compare la prima riga
`qmgr … from=<…>, size=…, nrcpt=… (queue active)` della sua istanza di coda.
`qmgr` riscrive la stessa riga **a ogni nuovo tentativo dalla coda deferred**
(109 Queue ID hanno più righe `queue active`): solo la prima conta.

`size=` è la dimensione del messaggio in coda. `nrcpt=` è il numero di
destinatari dell'istanza.

### 4.4 Messaggi già in coda prima dell'inizio del log

Alcune istanze iniziano direttamente con `qmgr … (queue active)` seguito da
tentativi con `delay=365945` (circa 4,2 giorni). Sono messaggi accettati prima
dell'inizio del file, ritentati ogni 70 minuti circa e infine scaduti:

```
qmgr  97DAF7F72F: from=<app@example.net>, size=24547, nrcpt=6 (queue active)
smtp  97DAF7F72F: host mx.example.com[203.0.113.145] said: 450 4.1.2 <user@example.com>: Recipient address rejected: Sender domain not found. (in reply to RCPT TO command)
smtp  97DAF7F72F: to=<user@example.com>, relay=mx.example.com[203.0.113.146]:25, delay=365945, …, dsn=4.1.2, status=deferred (…)
…
qmgr  97DAF7F72F: from=<app@example.net>, status=expired, returned to sender
bounce 97DAF7F72F: sender non-delivery notification: 6435C7F726
qmgr  97DAF7F72F: removed
```

La loro accettazione **non è osservata** e non viene conteggiata in alcun
periodo. I tentativi e gli esiti finali osservati vengono invece conteggiati
nel periodo in cui compaiono.

## 5. Eventi di consegna (righe `to=…, status=…`)

| Agente | `status=sent` | `deferred` | `bounced` | `deliverable` |
|---|---:|---:|---:|---:|
| `pipe` (`relay=filter`) | 13.687 | 0 | 0 | 0 |
| `smtp` | 11.220 | 2.069 | 1.874 | 0 |
| `lmtp` (Dovecot) | 2.455 | 1 | 0 | 23 |
| `error` | 0 | 588 | 0 | 0 |
| **Totale** | **27.362** | **2.658** | **1.874** | **23** |

Coppie DSN/status più frequenti: `2.0.0 sent` 17.597, `2.6.0 sent` 9.765,
`4.4.1 deferred` 2.394, `5.1.1 bounced` 1.843, `4.1.2 deferred` 165,
`2.1.5 deliverable` 23.

Formato generale:

```
to=<rcpt>, [orig_to=<alias>,] relay=<host[ip]:port | none | filter | host[private/dovecot-lmtp]>,
delay=<s>, delays=<a>/<b>/<c>/<d>, dsn=<x.y.z>, status=<sent|deferred|bounced|deliverable|…> (<testo>)
```

* `orig_to=` è presente in 4 righe.
* `delays=a/b/c/d`: a = prima del qmgr, b = in coda attiva, c = setup
  connessione, d = trasmissione.
* `postfix/error … status=deferred (delivery temporarily suspended: …)` è un
  differimento prodotto **senza tentativo di rete**, perché la destinazione
  è sospesa dopo errori precedenti. Viene contato tra i differimenti, ma
  separato per agente, e non tra i tentativi di rete.
* `status=deliverable` (23 casi, mittente `double-bounce@…`, DSN 2.1.5) indica
  **sonde di verifica indirizzo** (`verify`), non consegne. L'istanza che le
  contiene è classificata come sonda interna ed esclusa da messaggi e consegne.
* Righe `smtp … QID: host X[ip] said: …` senza `to=` (168) e
  `refused to talk to me` (65) riportano la risposta di **un singolo MX** prima
  del passaggio al successivo. Non sono esiti per destinatario. Sono
  conservate come diagnostica e non conteggiate come tentativi.

### 5.1 Differimenti e successi successivi

Ogni riga `status=deferred` è un **esito di tentativo**, non un esito finale.
L'esito finale osservabile per destinatario è l'ultimo stato definitivo:
`sent`, `bounced`, oppure `expired` (vedi sotto). Se non c'è stato definitivo
alla fine dei dati, il destinatario resta in elaborazione.

Nel file: pochi destinatari differiti e poi consegnati (circa 2), circa 90
coppie messaggio/destinatario solo differite (in gran parte messaggi già in
coda prima del log, poi scaduti).

### 5.2 Fallimenti definitivi

* `status=bounced` (1.874): rifiuto permanente del server remoto (`5.1.1`
  utente inesistente, `5.7.1`, `5.4.4`, …).
* `qmgr … status=expired, returned to sender` (29): superato
  `maximal_queue_lifetime`. **Tutti i destinatari ancora non consegnati**
  dell'istanza diventano falliti definitivamente in quel momento.

## 6. Notifiche generate (`postfix/bounce`)

| Riga | Conteggio | Significato |
|---|---:|---|
| `QID: sender non-delivery notification: NEWQID` | 1.888 | NDR (bounce) al mittente |
| `QID: sender delivery status notification: NEWQID` | 7 | DSN di successo (`NOTIFY=SUCCESS`) |

Un NDR copre **tutti** i destinatari falliti di un'istanza, quindi il numero
di notifiche (1.888) è diverso dal numero di destinatari falliti
(1.874 + scaduti). Il nuovo Queue ID è un messaggio generato da Postfix
(mittente `<>`), classificato come notifica e non come messaggio accettato.
Le sue consegne sono contate a parte.

Le DSN di successo vengono generate **già al passaggio nel filtro**, perché
per Postfix la consegna `pipe` è definitiva.

## 7. Rifiuti

| Tipo | Righe | Esempio anonimizzato |
|---|---:|---|
| `smtpd NOQUEUE: reject: RCPT` | 48 | `NOQUEUE: reject: RCPT from unknown[203.0.113.9]: 554 5.7.1 <x@example.com>: Relay access denied; from=<a@example.net> to=<x@example.com> proto=ESMTP helo=<h>` |
| `cleanup QID: milter-reject: END-OF-MESSAGE` codice 4xx | 946 | `milter-reject: END-OF-MESSAGE from mail-x.outbound.example.com[198.51.100.5]: 4.7.1 Try again later; from=<…> to=<…> proto=ESMTP helo=<…>` |
| `cleanup QID: milter-reject: END-OF-MESSAGE` codice 5xx | 80 | `… 5.7.1 Spam message rejected; …` |

* Il `milter-reject` avviene **dopo** l'assegnazione del Queue ID (`client=`)
  e prima di `qmgr`. Il messaggio **non è accettato**, quindi è un rifiuto SMTP
  *con* Queue ID. Il milter (`inet:…`) non è identificabile dal log: non ci
  sono righe OpenDKIM/OpenDMARC/rspamd.
* I rifiuti si distinguono in **temporanei** (4xx: il client ritenterà, e lo
  stesso messaggio può essere rifiutato più volte e poi accettato) e
  **permanenti** (5xx).
* Formati supportati ma non osservati (presi dalla documentazione Postfix):
  `QID: reject: RCPT|MAIL|DATA|…` di smtpd, `reject:`/`discard:`/`hold:` di
  `header_checks`/`body_checks`, consegne `postfix/discard`. Sono coperti da
  test con righe sintetiche.

## 8. SASL e TLS

* **SASL riuscite** (392 righe `client=` con SASL): campi `sasl_method=` e `sasl_username=` sulla riga
  `smtpd QID: client=…`.
* **SASL fallite** (1.691): `warning: unknown[192.0.2.40]: SASL LOGIN authentication failed: <motivo>`.
  Motivi: `UGFzc3dvcmQ6` (base64 di "Password:", 1.560), `VXNlcm5hbWU6`
  (base64 di "Username:", 21), `Connection lost to authentication server`
  (67), motivo vuoto per PLAIN (43). Il motivo base64 viene decodificato solo
  a scopo di classificazione.
* Riepilogo comandi di sessione in `disconnect from … ehlo=2 starttls=1 auth=0/1 … commands=N/M`.
  `auth=0/1` indica un tentativo AUTH fallito.
* **TLS**: non ci sono righe `TLS connection established`, perché
  `smtpd_tls_loglevel` e `smtp_tls_loglevel` sono a 0. L'uso di TLS in
  ingresso si ricava solo da `starttls=1` nella riga `disconnect`
  (12.732 sessioni). Per le consegne in uscita **non è disponibile** alcuna
  informazione TLS. Errori TLS osservati: `SSL_accept error from …`,
  `warning: TLS library problem: …no shared cipher…`.

## 9. Eventi senza Queue ID

| Evento | Righe | Trattamento |
|---|---:|---|
| `smtpd discarding EHLO keywords: CHUNKING` | 33.064 | rumore, non memorizzato |
| `smtpd connect from H[ip]` | 20.428 | sessione: conteggio connessioni |
| `smtpd disconnect from H[ip] … commands=…` | 20.426 | sessione: STARTTLS, AUTH, comandi |
| `smtpd warning: … SASL … authentication failed` | 1.691 | evento di sicurezza |
| `smtpd lost connection after X from …` / `timeout after X` | 177 | anomalia di sessione |
| `smtpd NOQUEUE: reject: …` | 48 | rifiuto SMTP |
| `smtp connect to H[ip]:25: Connection timed out` | 2.046 | diagnostica (il differimento per destinatario è altrove) |
| `smtpd warning: hostname … does not resolve …`, `non-SMTP command`, TLS | ~40 | anomalia/sicurezza |
| `anvil statistics`, `scache statistics` | 1.281 | informative, ignorate |
| `master`/`postfix-script` start/stop | 17 | ciclo di vita del servizio |
| `verify cache … cleanup` | 5 | informative |

## 10. Messaggi che attraversano più giornate

* Messaggi differiti ritentati per più giorni, con accettazione precedente
  all'inizio del log (§4.4).
* Il riavvio giornaliero delle 00:00 non chiude i messaggi in coda.
* Regola: ogni evento è attribuito al periodo del **proprio** timestamp.
  L'accettazione al momento del primo `queue active`, il tentativo al momento
  della riga di stato, il fallimento definitivo al momento di `bounced`/`expired`.
  Un messaggio accettato il 12 e consegnato il 13 conta come accettato il 12 e
  consegnato il 13.

## 11. Righe non riconosciute e limiti di interpretazione

* Le righe `cleanup … warning: header Subject: …` (26.683) sono azioni
  `WARN` di `header_checks` che registrano l'oggetto del messaggio. Non vengono
  memorizzate, per privacy e perché non portano informazioni statistiche
  aggiuntive (mittente e destinatario si trovano nelle righe qmgr e di consegna).
* `resent-message-id=` (3 casi) viene memorizzato come message-id alternativo.
* La natura del filtro `filter` (antispam, disclaimer, archiviazione…) non è
  deducibile dal log. Le 10 istanze passate al filtro senza reiniezione
  abbinabile e le reiniezioni senza originale sono riportate come anomalie
  di correlazione e non sono ipotizzate come "scartate".
* L'identità del milter non è deducibile.
* L'assenza di log TLS impedisce statistiche TLS in uscita.
* Il log contiene un solo host e una sola istanza: la correlazione per
  host/istanza è implementata e testata con dati sintetici.
* La prima ora (03:00, 11/10) e l'ultima (11:00, 13/10) sono coperte solo
  parzialmente.
* Ogni riga non classificata viene contata per processo durante l'import
  (`import_stats`) e riportata da `postfix-report verify`, senza attribuirle
  una semantica.

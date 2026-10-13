# LIMITATIONS.md — Limiti noti

## Interpretazione dei log

* **Anno dei timestamp BSD.** Il formato `Oct 11 03:22:02` non contiene
  l'anno. L'anno si deduce dalla data di modifica del file e dal passaggio
  dicembre→gennaio. Un file modificato molto dopo la sua ultima riga (copiato
  senza preservare la mtime) può essere datato male. In quel caso si imposta
  `input.year` oppure si configura rsyslog con timestamp RFC 3339
  (`RSYSLOG_FileFormat`), che il parser gestisce con anno e offset espliciti.
* **Ora legale.** Nella notte del ritorno all'ora solare l'ora 02 locale
  avviene due volte. Il parser distingue le due occorrenze grazie all'ordine
  delle righe (vincolo di monotonia). Nel report giornaliero l'ora 02 di quel
  giorno somma due ore reali; in primavera l'ora 02 non esiste.
* **Content filter.** La reiniezione è riconosciuta con `uid` e trasporto
  configurati (default dedotti dal log di riferimento: `uid=97`,
  `relay=filter`). Un filtro che reinietta via SMTP (`smtpd` su una porta
  dedicata, ad esempio amavis) non è riconosciuto come reiniezione: il
  messaggio risulterebbe accettato due volte. Va segnalato e gestito con una
  configurazione dedicata, non ancora implementata.
* **Message-id riutilizzati.** L'abbinamento originale → reiniezione usa
  mittente, message-id, numero di destinatari e ordine di arrivo. Con messaggi
  identici sotto tutti questi aspetti l'abbinamento segue l'ordine FIFO. I
  conteggi non cambiano; può cambiare solo quale dei messaggi identici è
  associato a quale consegna.
* **Messaggi già in coda all'inizio dei log.** La loro accettazione non è
  osservata e non viene contata. Tentativi ed esiti finali osservati invece sì,
  nel periodo in cui avvengono. Sono segnalati come "inizio non nei log".
* **Fallimenti per scadenza.** `status=expired` vale per i destinatari ancora
  pendenti **osservati**. Per un messaggio in coda da prima dell'inizio dei
  log, i destinatari mai ritentati nel periodo osservato non sono noti.
* **TLS.** Con `smtpd_tls_loglevel = 0` e `smtp_tls_loglevel = 0` (come nel log
  di riferimento) l'uso di TLS in ingresso si ricava solo dal contatore
  `starttls=` di fine sessione; per l'uscita non c'è alcun dato. Con
  `tls_loglevel ≥ 1` i report contano anche handshake e protocolli.
* **Formati non osservati.** `discard`, `hold`, `header_checks REJECT`, le
  consegne `postfix/discard` e le righe TLS dettagliate sono riconosciuti
  secondo il formato documentato da Postfix e coperti da test sintetici, ma non
  sono presenti nel log di riferimento. Le righe Postfix sconosciute sono
  contate (`verify`, "Qualità dei dati") e mai interpretate.
* **postscreen, OpenDKIM, OpenDMARC, amavis, rspamd** non producono metriche
  dedicate: non erano presenti nel log analizzato. Le loro righe, non essendo
  di Postfix, sono contate come "non Postfix".
* **Milter.** I `milter-reject` sono rifiuti SMTP con Queue ID. Il nome del
  milter non è deducibile dal log.

## Copertura

* La copertura è dedotta dalle righe presenti. Fra due righe consecutive
  distanti più di `coverage.gap_threshold` (default 60 minuti) c'è un buco
  dichiarato come "dati assenti". Su server con traffico molto basso, un'ora di
  silenzio reale verrebbe classificata come buco: in quel caso si aumenta la
  soglia.
* La prima e l'ultima ora di un log sono parziali: un'ora è "completa" solo se
  osservata dall'inizio alla fine.
* Con più host la copertura è l'unione delle righe di tutti gli host: un host
  silenzioso mentre gli altri scrivono non è rilevato come buco.

## Storico e dimensioni

* Gli eventi grezzi normalizzati restano nel database per consentire `rebuild`
  e la ricorrelazione dopo import arretrati. Non c'è una retention automatica.
  Al volume del server di riferimento (circa 150.000 righe al giorno) il
  database cresce di circa 30 MB al giorno, circa 11 GB all'anno. Per archivi
  lunghi conviene pianificare lo spazio o archiviare il database per anno.
* `rebuild` ricorrela a partire dagli eventi memorizzati, non dai file di log:
  un miglioramento del parser si applica ai dati vecchi solo reimportando i log
  originali in un database nuovo.

## Report

* I grafici richiedono JavaScript (locale, nessuna dipendenza). Senza
  JavaScript le pagine mostrano le tabelle dei dati di ogni grafico.
* Le classifiche settimanali, mensili e annuali sommano i conteggi giornalieri:
  sono esatte. I percentili dei ritardi sono calcolati esattamente sulle righe
  del periodo e non sono aggregabili fra periodi.
* Le variazioni rispetto al periodo precedente si mostrano come confrontabili
  solo se entrambi i periodi sono interamente coperti.
* Il font è quello di sistema (nessun font incorporato): l'aspetto può variare
  leggermente fra client.

## Esercizio

* Il lock è locale (`flock` su file): non protegge esecuzioni su host diversi
  che condividano lo stesso database via rete. È una configurazione da evitare
  con SQLite.
* Il binario richiede Linux (o un sistema con `flock(2)`).

# SPEC.md - Postfix Report

## Specifica di sviluppo per Claude Code

**Progetto:** postfix-report  
**Linguaggio:** Go  
**Database:** SQLite  
**Output principale:** report HTML statici  
**Esecuzione:** batch periodico mediante systemd timer o cron  
**Ambiente:** Linux, con particolare attenzione a Enterprise Linux  
**Input di riferimento:** `samples/maillog`

# 1. Obiettivo

Realizzare un'applicazione completa per analizzare i log di Postfix, conservare le statistiche storiche e produrre automaticamente report HTML statici.

Il programma deve consentire l'analisi del traffico di posta su cinque livelli:

1. Orario, con 24 intervalli per ciascuna giornata.
2. Giornaliero.
3. Settimanale, secondo ISO 8601.
4. Mensile.
5. Annuale.

Il report deve consentire di comprendere quantità, caratteristiche, prestazioni, anomalie e andamento storico del traffico SMTP.

L'intero sistema deve funzionare senza servizi esterni, senza connessione Internet e senza server applicativi.

# 2. Principi architetturali

Adottare la seguente pipeline:

```text
Postfix maillog
      |
      v
Streaming parser Go
      |
      v
Normalizzazione eventi
      |
      v
Correlazione Queue ID
      |
      v
Database SQLite
      |
      v
Aggregazioni statistiche
      |
      v
Generatore HTML
      |
      v
Directory report statici
      |
      +-- index.html
      +-- daily/
      +-- weekly/
      +-- monthly/
      +-- yearly/
      +-- assets/
```

Separare rigorosamente acquisizione, correlazione, persistenza, aggregazione e presentazione.

Il database SQLite è la fonte dei dati per la generazione dei report.

Non implementare un server HTTP applicativo, API REST o framework frontend che richiedano un processo server.

# 3. Analisi preliminare del maillog

Prima di sviluppare il parser definitivo, esaminare il file reale `samples/maillog`.

Identificare:

- Formato syslog e struttura temporale.
- Processi Postfix presenti.
- Tipologie di Queue ID.
- Eventi di accettazione e consegna.
- Eventi di rifiuto.
- Differimenti e fallimenti.
- Autenticazioni SASL.
- Eventi TLS.
- Eventuali filtri antispam.
- Eventuali componenti OpenDKIM/OpenDMARC.
- Messaggi con destinatari multipli.
- Messaggi che attraversano più giornate.
- Eventi senza Queue ID.

Produrre un documento `LOG_ANALYSIS.md` con classificazione degli eventi, esempi anonimizzati e limiti di interpretazione.

Non inventare semantiche per eventi sconosciuti.

# 4. Importazione incrementale

Implementare una CLI con almeno i seguenti comandi:

```bash
postfix-report import --file /var/log/maillog
postfix-report import --directory /var/log/mail
postfix-report import --file /var/log/maillog.1.gz

postfix-report generate --all
postfix-report generate --date 2026-10-12
postfix-report generate --week 2026-W42
postfix-report generate --month 2026-10
postfix-report generate --year 2026

postfix-report update
postfix-report rebuild
postfix-report verify
```

Il comando `update` deve:

1. Individuare i nuovi dati disponibili.
2. Importare gli eventi non ancora acquisiti.
3. Aggiornare le correlazioni.
4. Ricalcolare le aggregazioni interessate.
5. Rigenerare soltanto i report modificati.
6. Aggiornare gli indici di navigazione.
7. Terminare con un codice di uscita appropriato.

L'importazione deve essere idempotente.

Gestire rotazione dei log, file compressi, acquisizioni sovrapposte, interruzioni e ripartenze.

Utilizzare checkpoint persistenti e identificazione affidabile delle sorgenti.

Il parser deve lavorare in streaming.

# 5. Modello statistico

Distinguere obbligatoriamente:

- Messaggi accettati.
- Messaggi originati localmente.
- Messaggi ricevuti tramite SMTP.
- Destinatari.
- Tentativi di consegna.
- Consegne riuscite.
- Consegne differite.
- Fallimenti definitivi.
- Rifiuti SMTP.
- Bounce.
- Messaggi scartati.
- Messaggi ancora in elaborazione, quando ricostruibili.

Una consegna differita e successivamente riuscita non rappresenta un messaggio definitivamente fallito.

Un messaggio con più destinatari non deve essere conteggiato più volte come messaggio accettato.

Il Queue ID deve essere correlato considerando server, istanza, tempo e ciclo di vita del messaggio.

Gestire esplicitamente il possibile riutilizzo dei Queue ID.

Distinguere l'esito di un tentativo dall'esito finale osservabile.

Non attribuire a un periodo dati non effettivamente presenti.

# 6. Database SQLite

Progettare uno schema normalizzato con:

- Sorgenti dei log.
- Importazioni e checkpoint.
- Eventi.
- Messaggi.
- Destinatari.
- Tentativi di consegna.
- Eventi di sicurezza.
- Aggregazioni temporali.
- Stato di generazione dei report.

Prevedere migrazioni versionate.

Utilizzare transazioni e indici appropriati.

Mantenere le informazioni necessarie per ricalcolare statistiche e correlazioni.

Le aggregazioni devono poter essere ricostruite senza perdere coerenza.

# 7. Struttura dei report HTML

Generare una directory simile alla seguente:

```text
/var/www/html/postfix-report/
|
+-- index.html
+-- daily/
|   +-- 2026-10-11.html
|   +-- 2026-10-12.html
|
+-- weekly/
|   +-- 2026-W41.html
|   +-- 2026-W42.html
|
+-- monthly/
|   +-- 2026-09.html
|   +-- 2026-10.html
|
+-- yearly/
|   +-- 2025.html
|   +-- 2026.html
|
+-- assets/
    +-- css/
    +-- js/
    +-- fonts/
```

Tutte le pagine devono essere consultabili senza un backend.

Utilizzare percorsi relativi corretti, così da poter pubblicare la directory sotto qualsiasi prefisso URL.

Non utilizzare CDN.

I dati necessari a ciascun report devono essere incorporati nella pagina o distribuiti in asset statici locali.

Per garantire il funzionamento anche tramite `file://`, non richiedere chiamate `fetch()` per caricare dati indispensabili.

# 8. Design della dashboard

Realizzare un'interfaccia professionale, responsive, con impostazione simile a una console di monitoraggio.

Preferire:

- Sfondo chiaro.
- Tipografia leggibile.
- Grafici ben dimensionati.
- Tabelle compatte.
- Colori coerenti per gli stati.
- Navigazione semplice.
- Layout utilizzabile anche su smartphone.
- Modalità di stampa.

Non produrre una semplice sequenza di tabelle HTML.

## 8.1 Home page

`index.html` deve rappresentare il punto di accesso all'intero archivio.

Visualizzare:

- Data e ora dell'ultimo aggiornamento.
- Periodo coperto dai dati.
- Stato e completezza delle acquisizioni.
- Totale messaggi accettati.
- Totale consegne riuscite.
- Totale differimenti.
- Totale fallimenti definitivi.
- Totale rifiuti SMTP.
- Traffico degli ultimi sette giorni disponibili.
- Collegamenti agli ultimi report.
- Navigazione verso gli archivi mensili e annuali.

Le metriche devono riportare chiaramente la rispettiva unità di conteggio.

## 8.2 Report giornaliero

Visualizzare un grafico orario da 00 a 23 contenente:

- Messaggi accettati.
- Tentativi riusciti.
- Tentativi differiti.
- Fallimenti definitivi.
- Rifiuti SMTP.

Aggiungere:

- Volume in byte.
- Dimensione media dei messaggi.
- Ritardo medio.
- Percentili di consegna.
- Classifica domini mittenti.
- Classifica domini destinatari.
- Principali IP sorgenti.
- Principali motivi di rifiuto.
- Principali motivi di differimento.
- Statistiche TLS e SASL, quando disponibili.

## 8.3 Report settimanale

Visualizzare:

- Sette giorni da lunedì a domenica.
- Grafico del traffico giornaliero.
- Distribuzione oraria aggregata.
- Confronto con la settimana precedente.
- Variazione percentuale dei messaggi.
- Variazione percentuale dei rifiuti.
- Andamento dei ritardi.
- Classifiche settimanali.

Utilizzare settimane ISO 8601.

## 8.4 Report mensile

Visualizzare:

- Grafico del traffico per giorno.
- Totale messaggi.
- Totale destinatari.
- Volume dati.
- Rifiuti SMTP.
- Differimenti.
- Fallimenti definitivi.
- Tempi di consegna.
- Confronto con il mese precedente.
- Giorni con maggiore traffico.
- Distribuzione per giorno della settimana.

## 8.5 Report annuale

Visualizzare:

- Grafico mensile dei messaggi.
- Volume complessivo.
- Andamento dei rifiuti.
- Andamento dei differimenti.
- Andamento dei fallimenti.
- Tempi medi e percentili.
- Confronto con l'anno precedente.
- Classifiche annuali.
- Riepilogo dei dodici mesi.

# 9. Grafici e interattività offline

Utilizzare SVG oppure una libreria JavaScript distribuita localmente.

Prevedere:

- Tooltip.
- Legende.
- Possibilità di nascondere singole serie.
- Tabelle ordinabili.
- Filtri client-side sui dati già inclusi.
- Navigazione al periodo precedente e successivo.
- Collegamenti dai grafici mensili ai report giornalieri.
- Collegamenti dai grafici annuali ai report mensili.

Evitare di incorporare grandi quantità di eventi dettagliati nei file HTML.

I grafici devono utilizzare dati aggregati.

Non implementare filtri che richiedano interrogazioni SQL dal browser.

# 10. Gestione dei periodi incompleti

Un requisito fondamentale è distinguere zero eventi da dati mancanti.

Se un giorno non è stato acquisito, il report deve indicare che i dati sono assenti o che la copertura non è verificata.

Non mostrare automaticamente zero messaggi.

Se una settimana contiene soltanto due giorni acquisiti, il report deve riportare chiaramente la copertura disponibile.

Prevedere:

- Giorni acquisiti.
- Intervalli osservati.
- Periodi mancanti noti.
- Ultima importazione.
- Eventuali anomalie.

Non assumere che la presenza di un file dimostri la completezza di una giornata.

# 11. Generazione incrementale

Implementare un meccanismo di invalidazione dei report.

Quando vengono importati eventi relativi a una giornata, considerare per la rigenerazione:

- Report giornaliero interessato.
- Report settimanale corrispondente.
- Report mensile corrispondente.
- Report annuale corrispondente.
- Home page.

Se una correlazione modifica statistiche di periodi precedenti, invalidare anche tali periodi.

La rigenerazione deve avvenire tramite scrittura su file temporaneo e sostituzione atomica del file di destinazione.

Evitare che il web server esponga file HTML parzialmente scritti.

Non rigenerare l'intero archivio a ogni esecuzione.

# 12. Configurazione

Prevedere un file YAML o TOML.

Esempio orientativo:

```yaml
database:
  path: /var/lib/postfix-report/report.db

input:
  paths:
    - /var/log/maillog
    - /var/log/maillog.1
    - /var/log/maillog.*.gz
  timezone: Europe/Rome
  hostname: mail.example.org

output:
  directory: /var/www/html/postfix-report
  static: true
  minify: false

reports:
  daily: true
  weekly: true
  monthly: true
  yearly: true
  top_limit: 20

privacy:
  anonymize_email: false
  anonymize_ip: false

logging:
  level: info
```

Questo è un esempio di interfaccia di configurazione, non uno schema obbligatorio già implementato.

Definire e validare formalmente lo schema effettivo.

# 13. Esecuzione automatica

Preparare un'unità systemd oneshot e un timer.

L'obiettivo è eseguire periodicamente:

```bash
/usr/local/bin/postfix-report update
```

La frequenza deve essere configurabile.

Prevedere come impostazione iniziale un aggiornamento ogni ora.

Il programma deve evitare esecuzioni concorrenti incompatibili.

Implementare gestione corretta degli errori, codici di uscita e logging.

Fornire istruzioni per:

- Installazione del binario.
- Creazione delle directory.
- Permessi di lettura dei log.
- Permessi di scrittura SQLite.
- Permessi della directory HTML.
- Attivazione del timer.
- Pubblicazione mediante Apache o Nginx.

Non eseguire il programma come root se non strettamente necessario.

# 14. Sicurezza

I log contengono indirizzi email, IP e altri dati personali.

Applicare escaping HTML a tutti i dati.

Prevedere anonimizzazione configurabile.

Non pubblicare il database SQLite nella directory web.

Non pubblicare i log originali.

Non includere segreti nei report.

La directory HTML deve contenere esclusivamente risorse destinate alla consultazione.

Non presumere che i report siano pubblici: documentare come proteggerli tramite autenticazione e controllo degli accessi del web server.

# 15. Validazione e test

Creare test unitari, di integrazione ed end-to-end.

Verificare:

- Parsing dei formati osservati.
- Correlazione Queue ID.
- Destinatari multipli.
- Differimenti e successivi successi.
- Fallimenti definitivi.
- Rifiuti senza Queue ID.
- Messaggi tra giornate differenti.
- Rotazione dei log.
- Importazioni ripetute.
- Righe duplicate.
- Cambio di anno.
- Ora legale.
- Eventi incompleti.
- Aggregazioni temporali.
- Rigenerazione selettiva.
- Integrità dei file HTML.
- Collegamenti relativi.
- Navigazione offline.

Per il file reale produrre un documento `VALIDATION.md` contenente conteggi indipendenti e differenze eventualmente riscontrate.

Testare l'apertura dei report tramite `file://` e tramite un semplice web server statico.

# 16. Deliverable

Consegnare:

- Sorgenti Go.
- CLI completa.
- Parser.
- Correlatore.
- Database SQLite e migrazioni.
- Motore di aggregazione.
- Generatore HTML statico.
- Asset CSS e JavaScript locali.
- Template HTML.
- Configurazione di esempio.
- Unit test.
- Integration test.
- Test end-to-end.
- Benchmark.
- Makefile.
- README.md.
- METRICS.md.
- LOG_ANALYSIS.md.
- VALIDATION.md.
- LIMITATIONS.md.
- File systemd service e timer.
- Report HTML dimostrativi generati dai dati disponibili.

# 17. Modalità di sviluppo autonoma

Claude Code deve realizzare il progetto per fasi:

**Fase 1:** analisi del log reale e definizione delle metriche.

**Fase 2:** implementazione del parser e del modello dati.

**Fase 3:** correlazione eventi e importazione incrementale.

**Fase 4:** aggregazioni statistiche e verifica dei conteggi.

**Fase 5:** generazione delle dashboard HTML statiche.

**Fase 6:** aggiornamento automatico, invalidazione e rigenerazione selettiva.

**Fase 7:** test completi, benchmark e documentazione.

Alla fine di ogni fase verificare il codice mediante test automatizzati.

Procedere autonomamente, evitando di chiedere conferma per decisioni implementative ordinarie.

Non simulare funzionalità mancanti.

Non dichiarare completato il progetto prima di aver verificato i criteri di accettazione.

# 18. Criteri finali di accettazione

Il programma deve:

1. Importare correttamente il maillog fornito.
2. Non duplicare statistiche dopo importazioni ripetute.
3. Correlare gli eventi anche tra file differenti.
4. Produrre statistiche verificabili.
5. Conservare lo storico in SQLite.
6. Generare report giornalieri, settimanali, mensili e annuali.
7. Aggiornare soltanto i report interessati.
8. Funzionare senza connessione Internet.
9. Rendere navigabili i report tramite filesystem o web server statico.
10. Evidenziare dati mancanti e periodi incompleti.
11. Superare i test automatici.
12. Fornire documentazione sufficiente per l'esercizio in produzione.

**Priorità assoluta:** correttezza statistica, affidabilità dell'importazione e chiarezza dei report.

Il prodotto finale deve essere utilizzabile per analizzare anni di attività Postfix senza dipendere da un servizio applicativo permanente.


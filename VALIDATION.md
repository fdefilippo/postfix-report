# VALIDATION.md — Validazione delle statistiche sul maillog reale

Questo documento confronta le statistiche calcolate da `postfix-report` con
**conteggi indipendenti** ottenuti direttamente dal file `maillog` di riferimento
(254.884 righe, dall'11/10/2026 03:22:02 al 13/10/2026 11:32:40, host unico).

Esito complessivo: **nessuna differenza** su tutte le metriche, giornaliere e
totali; nessuna differenza su 399 valori orari, sui percentili dei ritardi e
sulle classifiche controllate.

Il confronto si riproduce con:

```bash
make validate      # usa samples/maillog oppure ./maillog
```

## 1. Metodo

Le verifiche indipendenti sono di tre tipi.

1. **Ricalcolo indipendente** (`scripts/independent_counts.py`): uno script
   Python che non condivide codice né struttura con il programma Go.
   * Legge il testo grezzo e attribuisce ogni riga al **giorno e all'ora locali
     scritti nel log**, senza conversioni di fuso. Il programma invece converte
     in UTC e poi torna all'ora locale: la concordanza verifica anche la gestione
     del tempo.
   * Usa una macchina a stati minimale per Queue ID: `client=`/`pickup` aprono
     il ciclo di vita, il primo `queue active` lo accetta, `removed` lo chiude.
   * Calcola i percentili con il metodo *nearest-rank* su tutte le righe
     `status=sent` non dirette al filtro.
2. **Conteggi diretti con `rg`/`awk`** sulle righe del log (§3).
3. **Controlli di coerenza interna** di `postfix-report verify` (§4) e test
   automatici di equivalenza fra import singolo e import incrementale con
   rotazione e compressione (§5).

Le definizioni delle metriche sono quelle di `METRICS.md` e sono comuni a
programma e script: la validazione controlla che il **calcolo** sia corretto.
Le **definizioni** (ad esempio la reiniezione del content filter) sono
giustificate dall'osservazione diretta del log in `LOG_ANALYSIS.md`.

## 2. Programma vs ricalcolo indipendente

Unità: messaggi (`msg_*`), destinatari (`rcpt_accepted`, `sent`, `bounced`,
`expired_rcpt`, `final_failures`), byte, eventi (`deferred`, `rejects*`,
`notifications`, `filter_handoffs`, `sasl_fail`), connessioni/sessioni.
I giorni 11 e 13 sono coperti solo in parte: il log inizia alle 03:22:02
dell'11 e finisce alle 11:32:40 del 13.

| Metrica | 2026-10-11 programma | 2026-10-11 indipendente | 2026-10-12 programma | 2026-10-12 indipendente | 2026-10-13 programma | 2026-10-13 indipendente | totale programma | totale indipendente | Esito |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| msg_accepted | 874 | 874 | 8.212 | 8.212 | 3.806 | 3.806 | 12.892 | 12.892 | OK |
| msg_smtp | 873 | 873 | 8.211 | 8.211 | 3.805 | 3.805 | 12.889 | 12.889 | OK |
| msg_local | 1 | 1 | 1 | 1 | 1 | 1 | 3 | 3 | OK |
| rcpt_accepted | 1.068 | 1.068 | 8.582 | 8.582 | 4.040 | 4.040 | 13.690 | 13.690 | OK |
| bytes_accepted | 414.983.981 | 414.983.981 | 1.849.011.987 | 1.849.011.987 | 1.012.852.836 | 1.012.852.836 | 3.276.848.804 | 3.276.848.804 | OK |
| sent | 1.068 | 1.068 | 8.580 | 8.580 | 4.027 | 4.027 | 13.675 | 13.675 | OK |
| deferred | 936 | 936 | 1.117 | 1.117 | 605 | 605 | 2.658 | 2.658 | OK |
| bounced | 2 | 2 | 1.551 | 1.551 | 321 | 321 | 1.874 | 1.874 | OK |
| expired_rcpt | 6 | 6 | 11 | 11 | 12 | 12 | 29 | 29 | OK |
| expired_msg | 6 | 6 | 11 | 11 | 12 | 12 | 29 | 29 | OK |
| final_failures | 8 | 8 | 1.562 | 1.562 | 333 | 333 | 1.903 | 1.903 | OK |
| rejects | 144 | 144 | 649 | 649 | 281 | 281 | 1.074 | 1.074 | OK |
| rejects_temp | 98 | 98 | 601 | 601 | 247 | 247 | 946 | 946 | OK |
| rejects_perm | 46 | 46 | 48 | 48 | 34 | 34 | 128 | 128 | OK |
| notifications | 6 | 6 | 1.562 | 1.562 | 327 | 327 | 1.895 | 1.895 | OK |
| filter_handoffs | 1.067 | 1.067 | 8.581 | 8.581 | 4.039 | 4.039 | 13.687 | 13.687 | OK |
| connections | 3.294 | 3.294 | 11.633 | 11.633 | 5.501 | 5.501 | 20.428 | 20.428 | OK |
| sessions | 3.294 | 3.294 | 11.633 | 11.633 | 5.499 | 5.499 | 20.426 | 20.426 | OK |
| sessions_starttls | 841 | 841 | 8.192 | 8.192 | 3.699 | 3.699 | 12.732 | 12.732 | OK |
| sasl_ok | 58 | 58 | 245 | 245 | 88 | 88 | 391 | 391 | OK |
| sasl_fail | 474 | 474 | 794 | 794 | 423 | 423 | 1.691 | 1.691 | OK |

| Ritardo consegne (s) | 2026-10-11 programma | 2026-10-11 indipendente | 2026-10-12 programma | 2026-10-12 indipendente | 2026-10-13 programma | 2026-10-13 indipendente | totale programma | totale indipendente | Esito |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| count | 1.068 | 1.068 | 8.580 | 8.580 | 4.027 | 4.027 | 13.675 | 13.675 | OK |
| avg | 0.979 | 0.979 | 1.5293 | 1.5293 | 1.4033 | 1.4033 | 1.4492 | 1.4492 | OK |
| p50 | 1 | 1 | 0.89 | 0.89 | 0.89 | 0.89 | 0.9 | 0.9 | OK |
| p90 | 1.7 | 1.7 | 2.3 | 2.3 | 1.8 | 1.8 | 2.1 | 2.1 | OK |
| p95 | 2.1 | 2.1 | 3.5 | 3.5 | 2.5 | 2.5 | 3 | 3 | OK |
| p99 | 3.2 | 3.2 | 20 | 20 | 5.6 | 5.6 | 15 | 15 | OK |
| max | 8.6 | 8.6 | 343 | 343 | 567 | 567 | 567 | 567 | OK |

Confronto orario: 399 valori (ora × metrica) confrontati, 0 differenze.
Classifiche (top 5 domini mittenti e IP sorgenti per giorno): 6 confronti, 0 differenze.

ESITO: nessuna differenza

Le classifiche confrontate sono i primi 5 domini mittenti e i primi 5 IP
sorgenti per giorno (valori e conteggi). Il confronto orario copre sette
metriche (accettati, consegne, differimenti, bounce, rifiuti, connessioni, SASL
fallite) per ogni ora con dati.

## 3. Conteggi diretti sul log (`rg` / `awk`)

| Verifica | Comando (sintesi) | Risultato | Programma |
|---|---|---:|---:|
| Consegne riuscite | `rg 'status=sent' \| rg -vc 'relay=filter'` | 13.675 | 13.675 |
| — di cui 11/10, 12/10, 13/10 | idem filtrando `^Oct DD` | 1.068 / 8.580 / 4.027 | 1.068 / 8.580 / 4.027 |
| Tentativi differiti | `rg -c 'status=deferred'` | 2.658 | 2.658 |
| Bounce | `rg -c 'status=bounced'` | 1.874 | 1.874 |
| Scadenze in coda | `rg -c 'status=expired'` | 29 | 29 |
| Rifiuti senza Queue ID | `rg -c 'NOQUEUE: reject'` | 48 | 48 (permanenti 5xx) |
| Rifiuti del milter | `rg -c 'milter-reject'` | 1.026 | 946 temporanei + 80 permanenti |
| Notifiche generate | `rg -c 'bounce[..]: QID: .* notification: '` | 1.895 | 1.895 |
| Connessioni SMTP | `rg -c 'smtpd[..]: connect from'` | 20.428 | 20.428 |
| SASL fallite | `rg -c 'authentication failed'` | 1.691 | 1.691 |
| Passaggi al content filter | `rg -c 'relay=filter'` | 13.687 | 13.687 |
| Reiniezioni dal filtro | `rg -c 'pickup[..]: QID: uid=97 '` | 12.889 | 12.889 collegate, 0 orfane |
| Sessioni con STARTTLS | `rg 'disconnect from' \| rg -c 'starttls=[1-9]'` | 12.732 | 12.732 |
| Ritardo medio consegne | `awk` su `delay=` delle righe `sent` non filtro | 1,449227 s su 13.675 | 1,4492 s su 13.675 |
| Queue ID con più `queue active` (ritentativi) | `rg -o … \| sort \| uniq -d` | 109 | contati una volta sola |

Quadratura dei messaggi SMTP: 13.923 righe `client=` = 12.889 accettati +
1.026 rifiutati dal milter dopo il DATA + 8 sessioni ancora in corso alla fine
del log (nessun `queue active`).

Quadratura dei destinatari: 13.690 destinatari accettati = 13.687 passaggi al
filtro (un passaggio per destinatario) + 3 destinatari dei 3 messaggi locali
(`uid=0`), che non passano dal filtro.

## 4. Controlli di coerenza (`postfix-report verify`)

Sul database ottenuto dal log reale tutti i controlli danno OK:

* `PRAGMA integrity_check` = ok, nessuna violazione di foreign key.
* Tutti gli eventi sono correlati. Ogni riga di stato ha la sua riga in
  `deliveries` (31.917 = 31.917), ogni rifiuto la sua in `rejects`
  (1.074 = 1.074), ogni evento di sicurezza la sua in `security_events` (1.893).
* Istanze: 13.923 `client=` → 13.923 istanze `smtp`; 12.892 `pickup` →
  12.892 istanze `local` + `reinject`; 1.895 notifiche → 1.895 istanze
  `notification`.
* Nessun messaggio originale con più di una reiniezione, nessuna reiniezione
  senza originale, nessun passaggio al filtro senza reiniezione.
* 0 righe Postfix non riconosciute su 254.884.
* Aggregati ricalcolati in memoria = aggregati memorizzati, per tutti i giorni.
* Report: tutte le pagine registrate esistono e tutti i link relativi puntano a
  file esistenti. Non ci sono riferimenti esterni, file temporanei residui o
  file estranei nella directory web.

## 5. Import incrementale, rotazione, ripetizioni

Il test `TestRealLogIncrementalWithRotation` (`internal/app/real_test.go`)
importa lo stesso log in cinque esecuzioni successive. Il file cresce, dopo la
terza esecuzione viene ruotato e compresso in `.gz`, e poi ne nasce uno nuovo.
I risultati sono **identici** a quelli dell'import in un solo passaggio, per
tutte le metriche della tabella del §2. `TestRealLogRegression` blocca questi
valori come test di regressione. Il test conserva solo numeri aggregati, nessun
dato del log.

I test di integrazione su log sintetici verificano inoltre: import ripetuti
(nessun evento nuovo, statistiche invariate), sorgenti sovrapposte (righe
duplicate riconosciute), righe identiche legittime nello stesso secondo
(conservate), import di dati arretrati (ricorrelazione completa, risultato
identico), riga parziale finale (letta solo quando completa), cambio d'anno,
ora legale e riuso dei Queue ID.

## 6. Differenze riscontrate

**Nessuna** nel confronto finale. Durante lo sviluppo i confronti hanno fatto
emergere e correggere questi problemi:

* Nel primo conteggio manuale i `disconnect` erano 20.345 anziché 20.426,
  perché il raggruppamento per pattern aveva separato alcune varianti.
  Il conteggio diretto con `rg` conferma 20.426, come il programma.
* Nell'import di dati arretrati, le righe dello stesso secondo provenienti da
  file diversi venivano ordinate per ordine di inserimento: una riga `removed`
  del file più recente poteva precedere una consegna dello stesso secondo nel
  file più vecchio. La correlazione ora ordina per timestamp, poi per sorgente
  più vecchia e posizione nel file.

## 7. Limiti della validazione

* Il log di riferimento copre circa 2,3 giorni, un solo host e una sola
  istanza. Settimane, mesi, anni, buchi di copertura, più host e cambio d'anno
  sono verificati con dati sintetici (`internal/testutil`, `cmd/synthlog`).
* Il riconoscimento della reiniezione (`uid=97`, trasporto `filter`) è stato
  dedotto da questo log. Su altri server va configurato
  (`correlation.reinject_uids`, `correlation.filter_transports`) e controllato
  con `verify` (reiniezioni orfane, passaggi al filtro senza reiniezione).
* Il log non contiene eventi TLS dettagliati né azioni `discard`/`hold`. I
  relativi parser sono verificati solo con righe sintetiche nel formato
  documentato da Postfix.

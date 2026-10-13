# METRICS.md — Definizione delle metriche

Ogni metrica indica **l'unità di conteggio** (messaggi, destinatari, eventi,
sessioni) e **l'istante** che la attribuisce a un periodo. I periodi sono
calcolati nel fuso configurato (`input.timezone`, default `Europe/Rome`).

## 1. Concetti

| Termine | Definizione |
|---|---|
| **Istanza di coda** | Ciclo di vita di un Queue ID su un host/istanza Postfix: dal primo evento al `removed`. Lo stesso Queue ID può generare più istanze (riutilizzo). |
| **Messaggio logico** | Un'istanza di coda *radice* più le eventuali istanze reiniettate dal content filter, collegate a essa. |
| **Origine** | `smtp` (ha `smtpd client=`), `local` (ha `pickup uid=` con uid non di reiniezione), `reinject` (pickup con uid di reiniezione, collegata al messaggio originale), `notification` (generata da `postfix/bounce`), `probe` (sonda di verifica, stato `deliverable`/`undeliverable`), `unknown` (accettazione non osservata, ad esempio un messaggio già in coda prima dell'inizio dei log). |
| **Handoff al filtro** | Riga `pipe`/consegna con `relay` uguale a un trasporto filtro configurato (default `filter`). Non è una consegna. |
| **Tentativo** | Riga `to=…, status=…` di un agente di consegna, esclusi gli handoff al filtro e le sonde. |

## 2. Metriche di messaggio (unità: messaggi)

| Metrica | Definizione | Istante |
|---|---|---|
| Messaggi accettati | Istanze radice con origine `smtp` o `local` per cui si osserva la prima riga `qmgr … (queue active)` | primo `queue active` |
| Ricevuti via SMTP | sottoinsieme degli accettati con origine `smtp` | idem |
| Originati localmente | sottoinsieme degli accettati con origine `local` | idem |
| Notifiche generate (bounce) | istanze con origine `notification`, separate per tipo (non-delivery, delay, success) | riga `postfix/bounce` |
| Messaggi scartati | azioni `discard` di cleanup/smtpd e consegne con agente `discard` (non presenti nel log di riferimento) | evento discard |
| Volume (byte) | somma di `size=` dei messaggi accettati (prima istanza, prima del filtro) | primo `queue active` |
| Dimensione media | volume / messaggi accettati | idem |
| Messaggi in elaborazione | messaggi accettati senza esito finale per tutti i destinatari e senza `removed` alla fine dei dati osservati | fine dei dati |

Un messaggio con più destinatari conta **una volta** tra gli accettati.
La reiniezione del filtro **non** genera un secondo messaggio.

## 3. Metriche di destinatario (unità: destinatari)

| Metrica | Definizione | Istante |
|---|---|---|
| Destinatari | `nrcpt` del primo `queue active` dei messaggi accettati | primo `queue active` |
| Consegne riuscite | righe `status=sent` (escluso l'handoff al filtro): ogni riga è un destinatario consegnato | riga di stato |
| Fallimenti definitivi | righe `status=bounced` + destinatari non ancora consegnati di un'istanza al momento di `qmgr … status=expired` | riga di stato / `expired` |
| Differiti poi consegnati | destinatari con almeno un `deferred` e poi un `sent` | riga `sent` |
| Esito finale | per destinatario: `delivered`, `failed`, `expired`, `pending` (nessun esito definitivo nei dati), `filtered` (consegnato al filtro, con l'esito proseguito nell'istanza reiniettata) | — |

## 4. Metriche di tentativo/evento (unità: eventi)

| Metrica | Definizione |
|---|---|
| Tentativi di consegna | righe di stato `sent` + `deferred` + `bounced` (esclusi gli handoff al filtro e le sonde) |
| Tentativi riusciti | `status=sent` |
| Tentativi differiti | `status=deferred`, separati per agente (`smtp`, `lmtp`, `error` = consegna sospesa senza tentativo di rete) |
| Handoff al filtro | righe con relay del filtro (diagnostica) |

Un differimento seguito da un successo conta come **un tentativo differito e
un tentativo riuscito**, ma **non** come fallimento definitivo.

## 5. Rifiuti SMTP (unità: eventi di rifiuto)

| Metrica | Definizione |
|---|---|
| Rifiuti SMTP | `smtpd NOQUEUE: reject:` + `smtpd QID: reject:` + `cleanup QID: milter-reject:` + `cleanup QID: reject:` (header/body checks) |
| — temporanei | codice SMTP 4xx |
| — permanenti | codice SMTP 5xx |

Un rifiuto è un **evento**: lo stesso messaggio rifiutato temporaneamente e
ritentato dal client produce più eventi. Le classifiche dei motivi usano il
testo normalizzato (indirizzi, IP e numeri lunghi sostituiti da segnaposto).

## 6. Sicurezza e sessioni (unità: eventi / sessioni)

| Metrica | Definizione |
|---|---|
| Connessioni SMTP | righe `smtpd connect from` |
| Sessioni con STARTTLS | righe `disconnect` con `starttls=N` e N ≥ 1 |
| Autenticazioni SASL riuscite | righe `client=` con `sasl_username=` |
| Autenticazioni SASL fallite | `warning: … SASL <method> authentication failed` |
| Sessioni interrotte | `lost connection after X`, `timeout after X` |
| Errori TLS | `SSL_accept error`, `TLS library problem` |

TLS in uscita: **non disponibile** se `smtp_tls_loglevel = 0`, come nel log di
riferimento.

## 7. Ritardi (unità: secondi, per destinatario consegnato)

* **Ritardo di consegna** è il campo `delay=` della riga `status=sent` finale
  (escluso l'handoff al filtro). Per i messaggi filtrati misura il tempo
  nell'istanza reiniettata.
* **Ritardo del filtro** è il campo `delay=` degli handoff al filtro, riportato a parte.
* I percentili p50, p90, p95 e p99 e il massimo sono calcolati **esattamente**
  sulle righe del periodo (metodo *nearest-rank*) e attribuiti all'istante
  della consegna.
* Il ritardo medio è la media aritmetica degli stessi valori.

## 8. Classifiche

| Classifica | Unità | Fonte |
|---|---|---|
| Domini mittenti | messaggi accettati | dominio di `from=` del primo `queue active` (`<>` → "(null sender)") |
| Domini destinatari | consegne riuscite | dominio di `to=` delle righe `sent` |
| IP sorgenti | messaggi accettati via SMTP | IP del `client=` |
| Motivi di rifiuto | eventi di rifiuto | testo normalizzato |
| Motivi di differimento | tentativi differiti | testo normalizzato (prefisso `host X said:` rimosso) |
| Utenti SASL | messaggi | `sasl_username` (anonimizzabile) |

## 9. Copertura

* **Intervallo osservato**: per ogni sorgente, i segmenti [prima riga, ultima riga]
  spezzati dove fra due righe consecutive passano più di
  `coverage.gap_threshold` (default 60 min). Un buco non è necessariamente
  traffico zero: è un periodo **non verificato**.
* Un'**ora** è `complete` se interamente coperta, `partial` se coperta in parte,
  `missing` se non coperta.
* Un **giorno** è `complete` se tutte le sue ore lo sono, `partial` se alcune
  sono coperte, `missing` altrimenti.
* Nei grafici le ore e i giorni `missing` sono mostrati come **dato assente**
  (barra assente e tratteggio), mai come zero. I totali di periodo riportano
  la copertura (es. "2 giorni su 7, di cui 1 parziale").
* Le variazioni percentuali rispetto al periodo precedente sono indicate come
  *non confrontabili* quando la copertura dei due periodi differisce.

## 10. Attribuzione temporale e casi limite

* Ogni metrica usa il timestamp del proprio evento, mai quello di un altro
  evento dello stesso messaggio.
* Anno: esplicito nei timestamp RFC 3339. Per il formato BSD si deduce dalla
  mtime del file e dai passaggi dicembre→gennaio.
* Ora legale: nella notte del cambio all'ora solare l'ora 02 locale contiene
  due ore reali. Il report giornaliero mostra 24 intervalli locali e il giorno
  ha 25 ore reali (23 in primavera). Gli istanti sono memorizzati in UTC.

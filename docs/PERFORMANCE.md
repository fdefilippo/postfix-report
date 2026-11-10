# Misure prestazionali dell'archivio

Misurazione locale del 10 novembre 2026, collegata ad A-06 (`mailstats-30i.3`).
Le misure precedono qualsiasi ottimizzazione SQL, cache o modifica dello schema.
Sono una baseline del working tree di remediation, basato su `5041ef8`, prima
dell'ulteriore consolidamento dei marker di ripresa A-02/A-05. Non rappresentano
misure della release finale né un confronto prestazionale prima/dopo quei fix.
Il risultato è una baseline ripetibile, non un dimensionamento di produzione.

## Riproduzione

Non occorrono servizi, log privati o nuovi strumenti. Usare la toolchain Go già
installata e una directory temporanea sotto `~/tmp`:

```bash
mkdir -p "$HOME/tmp/postfix-report-bench"
export TMPDIR="$HOME/tmp/postfix-report-bench" GOTMPDIR="$HOME/tmp/postfix-report-bench"
go test ./internal/query -run '^$' -bench 'Benchmark(Ranking|ExactDelays)' -benchmem -benchtime=1s -count=3
go test ./internal/report -run '^$' -bench 'Benchmark(ArchiveIndex|NoNewDataUpdate)' -benchmem -benchtime=1s -count=3
```

Eseguire i due comandi in sequenza. I benchmark non girano durante `go test`
ordinario: richiedono l'opzione `-bench`. I dati sono generati deterministicamente
in directory temporanee, senza modificare database o configurazioni operative.

## Metodo e limiti

Ambiente: Linux amd64, Intel N150, Go 1.27.3, `modernc.org/sqlite` v1.59.0
(SQLite 3.53.4). Database su file, WAL e `synchronous=NORMAL`, singola connessione.
Tre ripetizioni per caso, almeno un secondo cronometrato per ripetizione;
mediane nelle tabelle. Setup, popolamento e chiusura sono esclusi dal tempo per
operazione; il seed e la dimensione logica del DB sono riportati separatamente.
Il costo del filesystem e la contesa di un host di sviluppo possono variare:
non si tratta di un benchmark su host dedicato, né di un test sotto carico.

- **Indice:** pipeline reale import → correlazione → aggregazione su 3 e 5 anni
  dal 2020, un messaggio SMTP completo al giorno, UTC, con date RFC3339.
  I report per periodo sono disabilitati per isolare l'indice. `ArchiveIndex`
  costruisce il modello della pagina con tutte le query; `NoNewDataUpdate`
  include import senza nuove righe, controlli, rendering e confronto hash.
  Non include avvio CLI, acquisizione iniziale del lock o conversioni PDF/ODT.
  Gli update sono consecutivi: entro lo stesso secondo il banner dell'import
  può coincidere e saltare la riscrittura HTML. L'aggiornamento orario può invece
  riscriverlo; le query e il rendering sono comunque eseguiti.
- **Classifiche:** fixture isolata della tabella `agg_dim` di produzione, con
  quattro dimensioni e 64 chiavi stabili per giorno: 280576 righe in 3 anni,
  467712 in 5 anni. Query annuale e mensile riferite al 2022. Non rappresenta
  centinaia di migliaia di chiavi distinte. Sono verificati numero di chiavi,
  totale e prima riga restituiti. Il benchmark di scrittura sostituisce le 256
  righe di un giorno in una transazione, come nel ricalcolo degli aggregati.
- **Ritardi:** fixture isolata con 100000 e 1000000 consegne riuscite, query
  e indice temporale di produzione, distribuzione deterministica dei ritardi.
  Si verificano conteggio, mediana esatta e massimo. `B/op` misura allocazioni
  cumulative Go per chiamata, **non** picco heap vivo o RSS SQLite/processo.
  Non è stato provocato un OOM e non è definita una soglia di traffico sicura.

Le fixture isolate non sostituiscono `make validate`, né qualificano la stessa
scala di traffico sul server di produzione. I test nativi e sotto stress restano
nell'ambito separato di `mailstats-y7h`.

## Risultati

| Archivio, UTC | Costruzione modello indice | Update senza nuove righe | Allocazioni modello / update |
|---|---:|---:|---:|
| 3 anni, 1096 giorni | 35,32 ms | 55,99 ms | 2,32 / 2,88 MB |
| 5 anni, 1827 giorni | 78,24 ms | 130,28 ms | 3,89 / 4,46 MB |

Intervalli fra le tre ripetizioni: modello 3 anni 34,28–37,66 ms; modello
5 anni 72,75–149,31 ms; update 3 anni 49,21–87,75 ms; update 5 anni
93,80–146,65 ms. Questa variabilità limita la precisione delle stime e impedisce
di attribuire ogni differenza al solo numero di giorni. Le allocazioni
crescono insieme alla lunghezza dell'archivio; nessun riepilogo storico viene
riutilizzato prima delle query. Il confronto hash evita una riscrittura uguale,
ma arriva dopo query e rendering.

Classifiche su **5 anni, 467712 righe**, con conteggi identici in ogni caso:

| Indice | Annuale | Mensile | Dimensione DB |
|---|---:|---:|---:|
| PK `(day,dim,key)` | 22,12 ms | 1,63 ms | 21,91 MB |
| candidato `(dim,day,key)` | 32,01 ms | 2,49 ms | 43,11 MB |
| candidato `(dim,key,day)` | 362,99 ms | 407,47 ms | 43,64 MB |

La sostituzione di un giorno (256 righe) sul DB a 3 anni costa rispettivamente
**1,89 / 5,26 / 16,57 ms**. Le dimensioni sono `page_count * page_size` dopo
checkpoint, senza WAL, non un confronto fra backup compressi.

I piani SQL confermano:

- PK: ricerca per intervallo `day`, temporanei per `GROUP BY` e `ORDER BY`.
- `(dim,day,key)`: ricerca per dimensione e intervallo, ancora entrambi i temporanei.
- `(dim,key,day)`: ricerca per sola dimensione, nessun temporaneo per `GROUP BY`,
  ma scansione di tutta la storia di quella dimensione e temporaneo per `ORDER BY`.

Il candidato che elimina il temporaneo di raggruppamento è il più lento nel
workload misurato. Entrambi gli indici secondari non includono `value` e
richiedono accessi alla tabella; l'assenza di un temporaneo non equivale a un
miglioramento. Non è stata provata ogni combinazione di indice covering,
selettività o cardinalità.

| Consegne per chiamata `Delays` | Tempo | Allocazioni cumulative Go |
|---|---:|---:|
| 100000 | 202,16 ms | 5,70 MB |
| 1000000 | 1745,70 ms | 57,68 MB |

Il costo comprende lettura del driver, ordinamento SQL e materializzazione dei
campioni, con percentili esatti. Non è il solo costo della slice di float64;
il sort SQLite e la memoria del processo non sono descritti completamente da
`B/op`. Il limite di un milione di campioni è già applicato agli export PDF/ODT,
**sia web sia CLI**. HTML e `stats` restano privi di quel cap.

I log grezzi della sessione sono conservati localmente in
`review/evidence/epic-30i-performance-{query,report}.txt`; un parser produce
`epic-30i-performance-summary.json` con tutte le ripetizioni, mediane e intervalli.
La cartella `review/` è intenzionalmente ignorata da Git. Il primo run esplorativo
dell'indice si sovrapponeva all'inizio delle classifiche a 3 anni: è conservato
separatamente e non costituisce la baseline dell'indice qui riportata. La tabella
classifiche usa i casi a 5 anni eseguiti dopo quella sovrapposizione. Il run finale
dell'indice è stato eseguito dopo il completamento delle classifiche.

## Decisione

**Nessuna ottimizzazione di produzione e nessuna migrazione dello schema in
questa fase.** L'indice e l'update dipendono dallo storico, ma nel caso sintetico
misurato restano rispettivamente sotto 150 e 147 ms per chiamata. Questi valori
non giustificano ancora una cache con nuove regole d'invalidazione, soprattutto
per un aggiornamento orario e con il banner dell'ultima acquisizione che deve
restare aggiornato. Non sono limiti superiori validi per altro traffico.

Non adottare i due indici candidati: il workload misurato peggiora, la dimensione
quasi raddoppia e aumentano i costi di scrittura. Valutare altri indici soltanto
con una distribuzione rappresentativa dell'installazione e confronti completi
prima/dopo, mantenendo identici risultati e aggregati.

La crescita delle allocazioni dei ritardi è misurata; il rischio di esaurimento
memoria a volumi maggiori resta da qualificare. Il follow-up non bloccante
**`mailstats-b1q`**, collegato a `mailstats-y7h`, richiede misure su host dedicato
con alta cardinalità, heap vivo/RSS e costo del sort. Un eventuale streaming deve
preservare percentili nearest-rank esatti, filtri e snapshot coerente. Non sono
accettabili troncamenti o approssimazioni silenziose.

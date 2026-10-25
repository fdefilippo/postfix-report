// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// locale holds what changes with the report language (output.language):
// calendar names, date and number formats, and the translation catalog.
// The source text is Italian; other languages translate it through catalog.
type locale struct {
	lang        string // also the lang attribute of the pages
	months      []string
	monthsShort []string
	weekdays    []string // Monday first
	thousands   string
	decimal     string
	dayUnit     string // unit of durations of one day or more
	dateFmt     string // full date
	dayMonthFmt string // date without year
	tsFmt       string // date and time
	catalog     map[string]string
}

var locales = map[string]*locale{
	"it": {
		lang: "it",
		months: []string{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto",
			"settembre", "ottobre", "novembre", "dicembre"},
		monthsShort: []string{"gen", "feb", "mar", "apr", "mag", "giu", "lug", "ago", "set", "ott", "nov", "dic"},
		weekdays:    []string{"lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato", "domenica"},
		thousands:   ".", decimal: ",", dayUnit: "g",
		dateFmt: "02/01/2006", dayMonthFmt: "02/01", tsFmt: "02/01/2006 15:04:05",
	},
	"en": {
		lang: "en",
		months: []string{"January", "February", "March", "April", "May", "June", "July", "August",
			"September", "October", "November", "December"},
		monthsShort: []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
		weekdays:    []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
		thousands:   ",", decimal: ".", dayUnit: "d",
		dateFmt: "2006-01-02", dayMonthFmt: "Jan 2", tsFmt: "2006-01-02 15:04:05",
		catalog: english,
	},
}

// l returns the locale of the configured language (Italian if unknown:
// the configuration is validated before reports are generated).
func (g *Generator) l() *locale {
	if lc, ok := locales[g.Cfg.Output.Language]; ok {
		return lc
	}
	return locales["it"]
}

// tr translates an Italian source text. A text missing from the catalog is
// returned unchanged and recorded, so tests can require full coverage.
func (g *Generator) tr(s string) string {
	lc := g.l()
	if lc.catalog == nil {
		return s
	}
	if t, ok := lc.catalog[s]; ok {
		return t
	}
	if g.untranslated == nil {
		g.untranslated = map[string]bool{}
	}
	g.untranslated[s] = true
	return s
}

// trf translates an Italian format string, then formats it.
func (g *Generator) trf(format string, a ...any) string { return fmt.Sprintf(g.tr(format), a...) }

// Untranslated lists the texts rendered so far that have no translation in
// the configured language (always empty for Italian).
func (g *Generator) Untranslated() []string {
	var out []string
	for s := range g.untranslated {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func (g *Generator) monthName(m time.Month) string  { return g.l().months[m-1] }
func (g *Generator) monthShort(m time.Month) string { return g.l().monthsShort[m-1] }
func (g *Generator) weekday(t time.Time) string     { return g.l().weekdays[weekdayIndex(t)] }

// weekdayShort is the three-letter abbreviation of the weekday of t.
func (g *Generator) weekdayShort(t time.Time) string { return string([]rune(g.weekday(t))[:3]) }

// ---- numbers ----

func (g *Generator) groupThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(g.l().thousands)
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// fmtInt formats an integer with thousands separators.
func (g *Generator) fmtInt(v float64) string {
	return g.groupThousands(strconv.FormatInt(int64(math.Round(v)), 10))
}

// fmtDec formats with d decimals.
func (g *Generator) fmtDec(v float64, d int) string {
	s := strconv.FormatFloat(v, 'f', d, 64)
	ip, fp, ok := strings.Cut(s, ".")
	if !ok {
		return g.groupThousands(ip)
	}
	return g.groupThousands(ip) + g.l().decimal + fp
}

// fmtBytes formats a byte count with decimal units.
func (g *Generator) fmtBytes(v float64) string {
	units := []string{"B", "kB", "MB", "GB", "TB", "PB"}
	i := 0
	for math.Abs(v) >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if i == 0 {
		return g.fmtInt(v) + " B"
	}
	return g.fmtDec(v, 1) + " " + units[i]
}

// fmtSeconds formats a duration in seconds.
func (g *Generator) fmtSeconds(v float64) string {
	switch {
	case v < 60:
		return g.fmtDec(v, 2) + " s"
	case v < 3600:
		return g.fmtDec(v/60, 1) + " min"
	case v < 86400:
		return g.fmtDec(v/3600, 1) + " h"
	default:
		return g.fmtDec(v/86400, 1) + " " + g.l().dayUnit
	}
}

// fmtPct formats a percentage.
func (g *Generator) fmtPct(v float64) string { return g.fmtDec(v, 1) + "%" }

// fmtDelta formats a relative change and returns its CSS class.
func (g *Generator) fmtDelta(cur, prev float64) (string, string) {
	if prev == 0 {
		if cur == 0 {
			return g.fmtPct(0), "na"
		}
		return g.tr("n/d"), "na"
	}
	d := (cur - prev) / prev * 100
	s := g.fmtPct(d)
	if d > 0 {
		s = "+" + s
	}
	cls := "na"
	switch {
	case d > 0.05:
		cls = "up"
	case d < -0.05:
		cls = "down"
	}
	return s, cls
}

// english translates the Italian source texts of the pages, including the
// template. Format verbs must match the source.
var english = map[string]string{
	// page frame (template)
	"Navigazione principale": "Main navigation",
	"Periodi adiacenti":      "Adjacent periods",
	"Sezioni della pagina":   "Page sections",
	"Dati fino a %s":         "Data up to %s",
	"Generato il %s":         "Generated on %s",
	"I conteggi indicano sempre l'unità: messaggi, destinatari, tentativi, eventi o sessioni. Definizioni in METRICS.md.": "Counts always state their unit: messages, recipients, attempts, events or sessions. Definitions in METRICS.md.",
	"Il grafico richiede JavaScript: i dati sono nella tabella qui sotto.":                                                "The chart requires JavaScript: the data is in the table below.",
	"Mostra i dati del grafico": "Show the chart data",
	"Filtra…":                   "Filter…",
	"Filtra la tabella":         "Filter the table",
	"Nessun dato.":              "No data.",

	// navigation and titles
	"Panoramica":                         "Overview",
	"Giornaliero":                        "Daily",
	"Settimanale":                        "Weekly",
	"Mensile":                            "Monthly",
	"Annuale":                            "Yearly",
	"Anno %s":                            "Year %s",
	"settimana %s":                       "week %s",
	"anno %s":                            "year %s",
	"indirizzi e utenti pseudonimizzati": "addresses and users pseudonymized",
	"IP troncati":                        "IPs truncated",

	// coverage
	"Dati assenti: nessuna riga di log acquisita per questo periodo. I valori non sono zero: non sono disponibili.":                                                                                                                                "No data: no log lines were imported for this period. The values are not zero: they are unavailable.",
	"Copertura parziale: osservato il %s del periodo (%s). %s: %d con copertura completa, %d parziale, %d senza dati. I totali si riferiscono solo all'intervallo osservato; le variazioni rispetto al periodo precedente non sono confrontabili.": "Partial coverage: %s of the period observed (%s). %s: %d fully covered, %d partial, %d without data. Totals refer to the observed interval only; changes from the previous period are not comparable.",
	"Ore":                      "Hours",
	"Giorni":                   "Days",
	"Mesi":                     "Months",
	"nessun intervallo":        "no interval",
	"e altri %d intervalli":    "and %d more intervals",
	"dal %s al %s":             "from %s to %s",
	"Copertura":                "Coverage",
	"Copertura: %s · %s":       "Coverage: %s · %s",
	"completa":                 "complete",
	"parziale %s":              "partial %s",
	"ora inesistente":          "nonexistent hour",
	"dati assenti":             "no data",
	"n/d":                      "n/a",
	"ore con dati":             "hours with data",
	"giorni con dati":          "days with data",
	"mesi con dati":            "months with data",
	"Nessun dato da mostrare.": "No data to show.",
	"Intervallo":               "Interval",
	"Osservato":                "Observed",
	"Copertura per intervallo": "Coverage by interval",
	"Un intervallo senza righe di log non è traffico zero: è un dato assente.": "An interval without log lines is not zero traffic: the data is missing.",

	// KPIs and series
	"variazione non confrontabile":                 "change not comparable",
	"%s vs periodo prec.":                          "%s vs previous period",
	"Messaggi accettati":                           "Accepted messages",
	"messaggi · SMTP %s · locali %s":               "messages · SMTP %s · local %s",
	"Destinatari":                                  "Recipients",
	"destinatari dei messaggi accettati":           "recipients of accepted messages",
	"Consegne riuscite":                            "Successful deliveries",
	"destinatari consegnati · di cui notifiche %s": "recipients delivered · notifications %s",
	"Differimenti":                                 "Deferrals",
	"tentativi differiti · poi consegnati %s":      "deferred attempts · later delivered %s",
	"Fallimenti definitivi":                        "Permanent failures",
	"destinatari · bounce %s · scaduti %s":         "recipients · bounced %s · expired %s",
	"Rifiuti SMTP":                                 "SMTP rejections",
	"eventi · temporanei %s · permanenti %s":       "events · temporary %s · permanent %s",
	"Volume":                                "Volume",
	"byte dei messaggi accettati":           "bytes of accepted messages",
	"Dimensione media":                      "Average size",
	"per messaggio accettato":               "per accepted message",
	"Ritardo medio":                         "Average delay",
	"per consegna riuscita (campo delay)":   "per successful delivery (delay field)",
	"Notifiche generate":                    "Notifications generated",
	"messaggi bounce/DSN creati da Postfix": "bounce/DSN messages created by Postfix",
	"Messaggi scartati":                     "Discarded messages",
	"azioni discard":                        "discard actions",
	"Tentativi differiti":                   "Deferred attempts",
	"Volume accettato":                      "Accepted volume",
	"Periodo":                               "Period",

	// rankings
	"Domini mittenti":                          "Sender domains",
	"Domini destinatari":                       "Recipient domains",
	"IP sorgenti (SMTP)":                       "Source IPs (SMTP)",
	"Motivi di rifiuto SMTP":                   "SMTP rejection reasons",
	"Motivi di differimento":                   "Deferral reasons",
	"Motivi di fallimento (bounce)":            "Failure reasons (bounce)",
	"Relay di destinazione":                    "Destination relays",
	"IP rifiutati":                             "Rejected IPs",
	"Utenti SASL":                              "SASL users",
	"IP con autenticazioni SASL fallite":       "IPs with failed SASL authentications",
	"messaggi accettati":                       "accepted messages",
	"consegne riuscite":                        "successful deliveries",
	"eventi di rifiuto":                        "rejection events",
	"tentativi differiti":                      "deferred attempts",
	"destinatari":                              "recipients",
	"autenticazioni fallite":                   "failed authentications",
	"Valore":                                   "Value",
	"Nessun evento nel periodo.":               "No events in the period.",
	"Unità: %s. Totale %s, %d valori distinti": "Unit: %s. Total %s, %d distinct values",
	", mostrati i primi %d":                    ", top %d shown",
	"Primi %d valori per categoria; tabelle ordinabili e filtrabili.": "Top %d values per category; sortable and filterable tables.",

	// delays
	"Tipo":                          "Type",
	"Consegne":                      "Deliveries",
	"Media":                         "Mean",
	"Massimo":                       "Maximum",
	"Consegne riuscite (smtp/lmtp)": "Successful deliveries (smtp/lmtp)",
	"Passaggio al content filter":   "Hand-off to the content filter",
	"Tempi di consegna":             "Delivery times",
	"Campo delay= di Postfix, per destinatario consegnato; percentili esatti (nearest-rank) sulle consegne del periodo.": "Postfix delay= field, per delivered recipient; exact percentiles (nearest-rank) over the deliveries of the period.",
	"Ritardi di consegna":              "Delivery delays",
	"Mediana (p50)":                    "Median (p50)",
	"95° percentile":                   "95th percentile",
	"Andamento dei ritardi":            "Delay trend",
	"Tempi medi e percentili per mese": "Average times and percentiles by month",

	// security
	"Connessioni SMTP in ingresso":     "Inbound SMTP connections",
	"connessioni":                      "connections",
	"Sessioni concluse":                "Completed sessions",
	"sessioni":                         "sessions",
	"Sessioni con STARTTLS":            "Sessions with STARTTLS",
	"Messaggi con autenticazione SASL": "Messages with SASL authentication",
	"Autenticazioni SASL fallite":      "Failed SASL authentications",
	"eventi":                           "events",
	"Errori TLS":                       "TLS errors",
	"Sessioni interrotte (lost connection / timeout)":                                   "Interrupted sessions (lost connection / timeout)",
	"Handshake TLS registrati in ingresso / uscita":                                     "TLS handshakes logged inbound / outbound",
	"STARTTLS dalle statistiche di fine sessione di smtpd.":                             "STARTTLS from the smtpd end-of-session statistics.",
	"Nessun handshake TLS registrato (tls_loglevel = 0): TLS in uscita non misurabile.": "No TLS handshake logged (tls_loglevel = 0): outbound TLS cannot be measured.",
	"TLS e SASL": "TLS and SASL",
	"Indicatore": "Indicator",
	"Unità":      "Unit",
	"Sicurezza":  "Security",

	// composition and comparison
	"Categoria":                   "Category",
	"Voce":                        "Item",
	"Conteggio":                   "Count",
	"Istanze di coda per origine": "Queue instances by origin",
	"Righe di stato per agente":   "Status lines by agent",
	"Composizione del traffico":   "Traffic composition",
	"Le istanze 'reinject' sono il secondo passaggio dei messaggi dopo il content filter e non sono conteggiate come nuovi messaggi.": "'reinject' instances are the second pass of messages after the content filter and are not counted as new messages.",
	"messaggi":   "messages",
	"tentativi":  "attempts",
	"byte":       "bytes",
	"Metrica":    "Metric",
	"Variazione": "Change",
	"Variazioni calcolate sui periodi interamente osservati.":                               "Changes computed on fully observed periods.",
	"* Copertura diversa: periodo %s, precedente %s. Le variazioni non sono confrontabili.": "* Different coverage: period %s, previous %s. The changes are not comparable.",
	"Confronto con %s": "Comparison with %s",
	"Confronto":        "Comparison",

	// period pages
	"Indicatori del giorno": "Day indicators",
	"Variazione rispetto al giorno precedente, solo se entrambi i giorni sono interamente osservati.": "Change from the previous day, only if both days are fully observed.",
	"Traffico": "Traffic",
	"Ore locali 00–23. Le ore senza dati sono tratteggiate e non valgono zero.": "Local hours 00–23. Hours without data are hatched and do not count as zero.",
	"Traffico orario":                "Hourly traffic",
	"Volume orario":                  "Hourly volume",
	"Volume orario (byte accettati)": "Hourly volume (accepted bytes)",
	"Mittenti e destinatari":         "Senders and recipients",
	"Rifiuti ed errori":              "Rejections and errors",
	"Indicatori della settimana":     "Week indicators",
	"Variazione rispetto alla settimana precedente, solo se entrambe sono interamente osservate.": "Change from the previous week, only if both are fully observed.",
	"Da lunedì a domenica (settimana ISO 8601).":                                                  "Monday to Sunday (ISO 8601 week).",
	"Traffico giornaliero":                                             "Daily traffic",
	"Clic su un giorno per aprire il report giornaliero.":              "Click a day to open its daily report.",
	"Distribuzione oraria aggregata":                                   "Aggregated hourly distribution",
	"Somma, per ora del giorno, dei giorni osservati della settimana.": "Sum, by hour of the day, of the observed days of the week.",
	"Distribuzione oraria":                                             "Hourly distribution",
	"Indicatori del mese":                                              "Month indicators",
	"Variazione rispetto al mese precedente, solo se entrambi sono interamente osservati.": "Change from the previous month, only if both are fully observed.",
	"Traffico per giorno":          "Traffic by day",
	"Giorno":                       "Day",
	"Rifiuti":                      "Rejections",
	"Giorni con maggiore traffico": "Busiest days",
	"Ordinati per messaggi accettati; solo giorni con dati.": "Sorted by accepted messages; days with data only.",
	"Giorno della settimana":                                 "Day of the week",
	"Giorni con dati":                                        "Days with data",
	"Media per giorno":                                       "Average per day",
	"Distribuzione per giorno della settimana":               "Distribution by day of the week",
	"Media sui soli giorni con dati; * = include giorni a copertura parziale.": "Average over days with data only; * = includes partially covered days.",
	"Mese":                 "Month",
	"Messaggi":             "Messages",
	"Differiti":            "Deferred",
	"Fallimenti":           "Failures",
	"Indicatori dell'anno": "Year indicators",
	"Variazione rispetto all'anno precedente, solo se entrambi sono interamente osservati.": "Change from the previous year, only if both are fully observed.",
	"Messaggi per mese":                               "Messages by month",
	"Clic su un mese per aprire il report mensile.":   "Click a month to open its monthly report.",
	"Traffico mensile":                                "Monthly traffic",
	"Andamento di rifiuti, differimenti e fallimenti": "Rejections, deferrals and failures trend",
	"Anomalie mensili":                                "Monthly anomalies",
	"Volume mensile":                                  "Monthly volume",
	"Riepilogo mensile":                               "Monthly summary",
	"Riepilogo dei dodici mesi":                       "Twelve-month summary",

	// index
	"Nessun dato importato. Eseguire 'postfix-report update' oppure 'postfix-report import'.": "No data imported. Run 'postfix-report update' or 'postfix-report import'.",
	"Dati dal %s al %s": "Data from %s to %s",
	"Ultimo aggiornamento: %s (%s, esito %s; %s righe lette, %s eventi nuovi, %s duplicati ignorati).": "Last update: %s (%s, result %s; %s lines read, %s new events, %s duplicates ignored).",
	"Errore: %s":           "Error: %s",
	"Totali dell'archivio": "Archive totals",
	"Periodo coperto: %d giorni (%d completi, %d parziali, %d senza dati).": "Period covered: %d days (%d complete, %d partial, %d without data).",
	"Ultimi sette giorni": "Last seven days",
	"Giorni con almeno una riga di log; clic su un giorno per il dettaglio.": "Days with at least one log line; click a day for details.",
	"Traffico degli ultimi sette giorni disponibili":                         "Traffic of the last seven available days",
	"Report":   "Report",
	"Archivio": "Archive",
	"Mesi e anni disponibili; il numero indica i messaggi accettati.": "Available months and years; the number is the count of accepted messages.",
	"Report e acquisizioni":              "Reports and imports",
	"Ultimi report":                      "Latest reports",
	"Qualità dei dati":                   "Data quality",
	"Sorgente":                           "Source",
	"Righe":                              "Lines",
	"Dal":                                "From",
	"Al":                                 "To",
	"Stato":                              "State",
	"in crescita (letta fino alla fine)": "growing (read to the end)",
	"compressa":                          "compressed",
	", letta interamente":                ", fully read",
	"Stato delle acquisizioni":           "Import status",
	"Le sorgenti sono riconosciute dal contenuto: un file ruotato non viene reimportato.": "Sources are identified by their content: a rotated file is not imported again.",
	"Istanze il cui inizio non è nei log (già in coda prima dei dati)":                    "Instances whose start is not in the logs (already queued before the data)",
	"Queue ID riutilizzati senza 'removed' osservato":                                     "Queue IDs reused without an observed 'removed'",
	"Reiniezioni del filtro senza messaggio originale":                                    "Filter reinjections without an original message",
	"Scadenze senza destinatari pendenti osservati":                                       "Expirations without observed pending recipients",
	"Messaggi messi in hold":                                                              "Messages put on hold",
	"Istanze chiuse per inattività (stale_after)":                                         "Instances closed for inactivity (stale_after)",
	"Consegne senza 'queue active' osservato":                                             "Deliveries without an observed 'queue active'",
	"Condizione": "Condition",
	"Istanze di coda ancora aperte alla fine dei dati (messaggi in elaborazione)":    "Queue instances still open at the end of the data (messages in progress)",
	"Destinatari senza esito definitivo":                                             "Recipients without a final outcome",
	"Messaggi passati al filtro senza reiniezione osservata":                         "Messages passed to the filter without an observed reinjection",
	"Righe Postfix non riconosciute":                                                 "Unrecognized Postfix lines",
	"Interruzioni nella copertura (buchi > soglia)":                                  "Coverage interruptions (gaps > threshold)",
	"Condizioni che limitano l'interpretazione; non sono attribuite ad alcun esito.": "Conditions that limit the interpretation; they are not attributed to any outcome.",
	"Buchi: %s.":        "Gaps: %s.",
	"Anomalie e limiti": "Anomalies and limits",
	"Giorni acquisiti":  "Imported days",
	"Ultimi giorni dell'archivio. Un giorno è completo solo se tutte le sue ore sono state osservate.": "Latest days of the archive. A day is complete only if all its hours were observed.",
}

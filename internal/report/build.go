// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	ag "github.com/fdefilippo/postfix-report/internal/aggregate"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/query"
)

var monthNames = []string{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto",
	"settembre", "ottobre", "novembre", "dicembre"}
var monthShort = []string{"gen", "feb", "mar", "apr", "mag", "giu", "lug", "ago", "set", "ott", "nov", "dic"}
var weekdayNames = []string{"lunedì", "martedì", "mercoledì", "giovedì", "venerdì", "sabato", "domenica"}

// weekdayIndex returns 0 for Monday … 6 for Sunday.
func weekdayIndex(t time.Time) int { return (int(t.Weekday()) + 6) % 7 }

func (g *Generator) title(p period.Period) string {
	switch p.Kind {
	case period.Day:
		return fmt.Sprintf("%s %d %s %d", weekdayNames[weekdayIndex(p.Start)], p.Start.Day(), monthNames[p.Start.Month()-1], p.Start.Year())
	case period.Week:
		last := p.End.Add(-time.Hour)
		return fmt.Sprintf("Settimana %s (%d/%02d – %d/%02d/%d)", p.Key, p.Start.Day(), int(p.Start.Month()),
			last.Day(), int(last.Month()), last.Year())
	case period.Month:
		return strings.ToUpper(monthNames[p.Start.Month()-1][:1]) + monthNames[p.Start.Month()-1][1:] + " " + p.Start.Format("2006")
	default:
		return "Anno " + p.Key
	}
}

func (g *Generator) shortTitle(p period.Period) string {
	switch p.Kind {
	case period.Day:
		return p.Start.Format("02/01/2006")
	case period.Month:
		return monthShort[p.Start.Month()-1] + " " + p.Start.Format("2006")
	default:
		return p.Key
	}
}

// href returns the link from a page at depth root to period p, or "" if p is
// outside the data range (no page exists).
func (g *Generator) href(root string, p period.Period) string {
	if !g.inRange(p) || !g.enabled(p.Kind) {
		return ""
	}
	return root + p.Path()
}

func (g *Generator) link(root string, p period.Period, text string) *Link {
	if h := g.href(root, p); h != "" {
		return &Link{Href: h, Text: text}
	}
	return nil
}

// nav builds the top bar. On the index it points to the latest periods; on a
// period page it points to the periods containing that page, so that pages
// which are not regenerated never carry stale "latest" links.
func (g *Generator) nav(root string, active string, at time.Time) []NavItem {
	items := []NavItem{{Href: root + "index.html", Text: "Panoramica", Active: active == "index"}}
	if !g.hasData {
		return items
	}
	names := map[period.Kind]string{period.Day: "Ultimo giorno", period.Week: "Ultima settimana", period.Month: "Ultimo mese", period.Year: "Ultimo anno"}
	if active != "index" {
		names = map[period.Kind]string{period.Day: "Giorno", period.Week: "Settimana", period.Month: "Mese", period.Year: "Anno"}
	}
	for _, k := range period.Kinds {
		if !g.enabled(k) {
			continue
		}
		// A period page links to itself and to the coarser periods containing it.
		if active != "index" && indexOf(string(k)) < indexOf(active) {
			continue
		}
		p := period.Of(k, at, g.loc())
		if h := g.href(root, p); h != "" {
			items = append(items, NavItem{Href: h, Text: names[k], Active: active == string(k)})
		}
	}
	return items
}

func indexOf(kind string) int {
	for i, k := range period.Kinds {
		if string(k) == kind {
			return i
		}
	}
	return 0
}

func (g *Generator) basePage(root, title, active string, at time.Time) *Page {
	return &Page{
		Root: root, SiteTitle: g.Cfg.Output.Title, Title: title, Nav: g.nav(root, active, at),
		Generated: generatedPlaceholder, Version: g.Version, Privacy: g.privacyNote(),
	}
}

func (g *Generator) privacyNote() string {
	var p []string
	if g.Cfg.Privacy.AnonymizeEmail {
		p = append(p, "indirizzi e utenti pseudonimizzati")
	}
	if g.Cfg.Privacy.AnonymizeIP {
		p = append(p, "IP troncati")
	}
	return strings.Join(p, ", ")
}

// periodData gathers what every period page needs.
type periodData struct {
	p        period.Period
	days     []string
	totals   query.Metrics
	cov      query.Bucket
	children []query.Bucket // hours (day), days (week/month), months (year)
}

func (g *Generator) load(p period.Period) (*periodData, error) {
	pd := &periodData{p: p, days: p.Days()}
	var err error
	if pd.totals, err = g.Q.Totals(pd.days); err != nil {
		return nil, err
	}
	ivs, err := g.Q.Intervals(p.Start.Unix(), p.End.Unix())
	if err != nil {
		return nil, err
	}
	pd.cov = coverageOf(ivs, p.Start.Unix(), p.End.Unix())
	switch p.Kind {
	case period.Day:
		pd.children, err = g.Q.HourBuckets(p)
	case period.Week, period.Month:
		pd.children, err = g.Q.DayBuckets(p)
	default:
		pd.children, err = g.Q.MonthBuckets(p)
	}
	return pd, err
}

func coverageOf(ivs []query.Interval, lo, hi int64) query.Bucket {
	var c int64
	for _, iv := range ivs {
		s, e := max(iv.Start, lo), min(iv.End+1, hi)
		if e > s {
			c += e - s
		}
	}
	b := query.Bucket{Start: lo, End: hi, Covered: c}
	switch {
	case c >= hi-lo:
		b.Status, b.Fraction = query.Complete, 1
	case c <= 0:
		b.Status = query.Missing
	default:
		b.Status, b.Fraction = query.Partial, float64(c)/float64(hi-lo)
	}
	return b
}

// coverageBanners describes the coverage of a period.
func (g *Generator) coverageBanners(pd *periodData) []Banner {
	switch pd.cov.Status {
	case query.Missing:
		return []Banner{{"bad", "Dati assenti: nessuna riga di log acquisita per questo periodo. I valori non sono zero: non sono disponibili."}}
	case query.Partial:
		var full, part, miss int
		for _, b := range pd.children {
			switch b.Status {
			case query.Complete:
				full++
			case query.Partial:
				part++
			case query.Missing:
				miss++
			}
		}
		unit := map[period.Kind]string{period.Day: "ore", period.Week: "giorni", period.Month: "giorni", period.Year: "mesi"}[pd.p.Kind]
		obs := g.observedRange(pd.p)
		return []Banner{{"warn", fmt.Sprintf("Copertura parziale: osservato il %s del periodo (%s). %s: %d con copertura completa, %d parziale, %d senza dati. I totali si riferiscono solo all'intervallo osservato; le variazioni rispetto al periodo precedente non sono confrontabili.",
			fmtPct(pd.cov.Fraction*100), obs, strings.ToUpper(unit[:1])+unit[1:], full, part, miss)}}
	}
	return nil
}

func (g *Generator) observedRange(p period.Period) string {
	ivs, err := g.Q.Intervals(p.Start.Unix(), p.End.Unix())
	if err != nil || len(ivs) == 0 {
		return "nessun intervallo"
	}
	var parts []string
	for i, iv := range ivs {
		if i == 4 {
			parts = append(parts, fmt.Sprintf("e altri %d intervalli", len(ivs)-4))
			break
		}
		s, e := max(iv.Start, p.Start.Unix()), min(iv.End, p.End.Unix()-1)
		parts = append(parts, "dal "+g.ts(s)+" al "+g.ts(e))
	}
	return strings.Join(parts, "; ")
}

func (g *Generator) ts(u int64) string {
	return time.Unix(u, 0).In(g.loc()).Format("02/01/2006 15:04:05")
}

// ---- KPIs ----

func (g *Generator) kpis(t query.Metrics, prev query.Metrics, comparable bool) Block {
	delta := func(k string) (string, string) {
		if prev == nil {
			return "", ""
		}
		if !comparable {
			return "variazione non confrontabile", "na"
		}
		d, c := fmtDelta(t.Get(k), prev.Get(k))
		return d + " vs periodo prec.", c
	}
	mk := func(label, metric, unit, class string, value string) KPI {
		k := KPI{Label: label, Value: value, Unit: unit, Class: class}
		if metric != "" {
			k.Delta, k.DeltaClass = delta(metric)
		}
		return k
	}
	acc := t.Get(ag.MAccepted)
	avgSize, avgDelay := "n/d", "n/d"
	if acc > 0 {
		avgSize = fmtBytes(t.Get(ag.MBytes) / acc)
	}
	if n := t.Get(ag.MDelayCount); n > 0 {
		avgDelay = fmtSeconds(t.Get(ag.MDelaySum) / n)
	}
	ks := []KPI{
		mk("Messaggi accettati", ag.MAccepted, fmt.Sprintf("messaggi · SMTP %s · locali %s", fmtInt(t.Get(ag.MAcceptedSMTP)), fmtInt(t.Get(ag.MAcceptedLocal))), "accepted", fmtInt(acc)),
		mk("Destinatari", ag.MRecipients, "destinatari dei messaggi accettati", "accepted", fmtInt(t.Get(ag.MRecipients))),
		mk("Consegne riuscite", ag.MSent, fmt.Sprintf("destinatari consegnati · di cui notifiche %s", fmtInt(t.Get(ag.MSentNotif))), "sent", fmtInt(t.Get(ag.MSent))),
		mk("Differimenti", ag.MDeferred, fmt.Sprintf("tentativi differiti · poi consegnati %s", fmtInt(t.Get(ag.MSentAfterDefer))), "deferred", fmtInt(t.Get(ag.MDeferred))),
		mk("Fallimenti definitivi", ag.MFinalFailures, fmt.Sprintf("destinatari · bounce %s · scaduti %s", fmtInt(t.Get(ag.MBounced)), fmtInt(t.Get(ag.MExpiredRcpt))), "failed", fmtInt(t.Get(ag.MFinalFailures))),
		mk("Rifiuti SMTP", ag.MRejects, fmt.Sprintf("eventi · temporanei %s · permanenti %s", fmtInt(t.Get(ag.MRejectsTemp)), fmtInt(t.Get(ag.MRejectsPerm))), "rejected", fmtInt(t.Get(ag.MRejects))),
		mk("Volume", ag.MBytes, "byte dei messaggi accettati", "", fmtBytes(t.Get(ag.MBytes))),
		mk("Dimensione media", "", "per messaggio accettato", "", avgSize),
		mk("Ritardo medio", "", "per consegna riuscita (campo delay)", "", avgDelay),
		mk("Notifiche generate", ag.MNotifications, "messaggi bounce/DSN creati da Postfix", "", fmtInt(t.Get(ag.MNotifications))),
	}
	if d := t.Get(ag.MDiscarded); d > 0 {
		ks = append(ks, mk("Messaggi scartati", ag.MDiscarded, "azioni discard", "", fmtInt(d)))
	}
	return Block{Kind: "kpis", KPIs: ks}
}

// ---- charts ----

type seriesDef struct {
	name, metric, color string
}

var trafficSeries = []seriesDef{
	{"Messaggi accettati", ag.MAccepted, colAccepted},
	{"Consegne riuscite", ag.MSent, colSent},
	{"Tentativi differiti", ag.MDeferred, colDeferred},
	{"Fallimenti definitivi", ag.MFinalFailures, colFailed},
	{"Rifiuti SMTP", ag.MRejects, colRejected},
}

// bucketChart builds a chart over buckets; values of buckets without data are null.
func (g *Generator) bucketChart(id, title, typ, unit string, buckets []query.Bucket, labels, short []string,
	links []string, values []query.Metrics, defs []seriesDef) Block {
	spec := chartSpec{Type: typ, Title: title, Labels: labels, Short: short, Unit: unit}
	for i, b := range buckets {
		spec.Status = append(spec.Status, b.Status)
		spec.Fraction = append(spec.Fraction, round3(b.Fraction))
		if b.Status == query.Missing || b.Status == "nonexistent" {
			spec.HasMissing = true
		}
		if links != nil {
			if links[i] != "" {
				spec.Links = append(spec.Links, strp(links[i]))
			} else {
				spec.Links = append(spec.Links, nil)
			}
		}
	}
	head := []Th{{Text: "Periodo"}, {Text: "Copertura"}}
	for _, d := range defs {
		head = append(head, Th{Text: d.name, Num: true})
	}
	tbl := &Table{ID: id + "-table", Head: head}
	for _, d := range defs {
		s := seriesSpec{Name: d.name, Color: d.color}
		for i, b := range buckets {
			if b.Status == query.Missing || b.Status == "nonexistent" {
				s.Values = append(s.Values, nil)
				continue
			}
			s.Values = append(s.Values, f64(round3(values[i].Get(d.metric))))
		}
		spec.Series = append(spec.Series, s)
	}
	for i, b := range buckets {
		row := Tr{Class: b.Status}
		cell := Td{Text: labels[i]}
		if links != nil && links[i] != "" {
			cell.Href = links[i]
		}
		row.Cells = append(row.Cells, cell, Td{Text: statusText(b), Badge: badgeClass(b.Status)})
		for _, d := range defs {
			if b.Status == query.Missing || b.Status == "nonexistent" {
				row.Cells = append(row.Cells, Td{Text: "n/d", Num: true})
				continue
			}
			v := values[i].Get(d.metric)
			row.Cells = append(row.Cells, Td{Text: fmtUnit(v, unit), Num: true, Sort: fmt.Sprint(v)})
		}
		tbl.Rows = append(tbl.Rows, row)
	}
	return Block{Kind: "chart", Chart: &Chart{ID: id, JSON: toJS(spec), Table: tbl}}
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

func fmtUnit(v float64, unit string) string {
	switch unit {
	case "bytes":
		return fmtBytes(v)
	case "s":
		return fmtSeconds(v)
	}
	if v != float64(int64(v)) {
		return fmtDec(v, 2)
	}
	return fmtInt(v)
}

func statusText(b query.Bucket) string {
	switch b.Status {
	case query.Complete:
		return "completa"
	case query.Partial:
		return "parziale " + fmtPct(b.Fraction*100)
	case "nonexistent":
		return "ora inesistente"
	default:
		return "dati assenti"
	}
}

func badgeClass(s string) string {
	switch s {
	case query.Complete, query.Partial:
		return s
	}
	return "missing"
}

// ---- rankings ----

type rankDef struct {
	dim, title, unit string
	anon             string // "ip", "user" or ""
}

var rankings = []rankDef{
	{ag.DSenderDomain, "Domini mittenti", "messaggi accettati", ""},
	{ag.DRcptDomain, "Domini destinatari", "consegne riuscite", ""},
	{ag.DClientIP, "IP sorgenti (SMTP)", "messaggi accettati", "ip"},
	{ag.DRejectReason, "Motivi di rifiuto SMTP", "eventi di rifiuto", ""},
	{ag.DDeferReason, "Motivi di differimento", "tentativi differiti", ""},
	{ag.DBounceReason, "Motivi di fallimento (bounce)", "destinatari", ""},
	{ag.DRelay, "Relay di destinazione", "consegne riuscite", ""},
	{ag.DRejectIP, "IP rifiutati", "eventi di rifiuto", "ip"},
	{ag.DSASLUser, "Utenti SASL", "messaggi accettati", "user"},
	{ag.DSASLFailIP, "IP con autenticazioni SASL fallite", "autenticazioni fallite", "ip"},
}

func (g *Generator) rankingCards(idPrefix string, days []string, defs []rankDef) ([]Block, error) {
	if len(days) == 0 {
		return nil, nil
	}
	var cards []Block
	for _, d := range defs {
		limit := g.Cfg.Reports.TopLimit
		anonIP := d.anon == "ip" && g.Cfg.Privacy.AnonymizeIP
		anonUser := d.anon == "user" && g.Cfg.Privacy.AnonymizeEmail
		fetch := limit
		if anonIP {
			fetch = 1 << 30 // merge after truncation, then cut
		}
		items, total, distinct, err := g.Q.Ranking(days[0], days[len(days)-1], d.dim, fetch)
		if err != nil {
			return nil, err
		}
		if anonIP || anonUser {
			merged := map[string]int64{}
			for _, it := range items {
				k := it.Key
				if anonIP {
					k = g.Anon.IP(k)
				} else {
					k = g.Anon.User(k)
				}
				merged[k] += it.Value
			}
			items = items[:0]
			for k, v := range merged {
				items = append(items, query.RankItem{Key: k, Value: v})
			}
			sort.Slice(items, func(i, j int) bool {
				if items[i].Value != items[j].Value {
					return items[i].Value > items[j].Value
				}
				return items[i].Key < items[j].Key
			})
			if anonIP {
				distinct = len(items)
			}
			if len(items) > limit {
				items = items[:limit]
			}
		}
		tbl := &Table{ID: idPrefix + "-" + d.dim, Sortable: true, Filter: len(items) > 8,
			Head:  []Th{{Text: "#", Num: true}, {Text: "Valore"}, {Text: d.unit, Num: true}, {Text: "%", Num: true}},
			Empty: "Nessun evento nel periodo."}
		var maxV int64 = 1
		if len(items) > 0 {
			maxV = items[0].Value
		}
		for i, it := range items {
			pct := 0.0
			if total > 0 {
				pct = float64(it.Value) / float64(total) * 100
			}
			tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{
				{Text: fmt.Sprint(i + 1), Num: true, Sort: fmt.Sprint(i + 1)},
				{Text: it.Key, Key: true},
				{Text: fmtInt(float64(it.Value)), Num: true, Sort: fmt.Sprint(it.Value), Bar: float64(it.Value) / float64(maxV)},
				{Text: fmtPct(pct), Num: true, Sort: fmt.Sprintf("%.4f", pct)},
			}})
		}
		note := fmt.Sprintf("Unità: %s. Totale %s, %d valori distinti", d.unit, fmtInt(float64(total)), distinct)
		if distinct > len(items) {
			note += fmt.Sprintf(", mostrati i primi %d", len(items))
		}
		cards = append(cards, Block{Kind: "card", Title: d.title, Note: note + ".", Children: []Block{{Kind: "table", Table: tbl}}})
	}
	return cards, nil
}

// ---- delays ----

func (g *Generator) delayTable(id string, lo, hi int64) (Block, error) {
	tbl := &Table{ID: id, Head: []Th{{Text: "Tipo"}, {Text: "Consegne", Num: true}, {Text: "Media", Num: true},
		{Text: "p50", Num: true}, {Text: "p90", Num: true}, {Text: "p95", Num: true}, {Text: "p99", Num: true}, {Text: "Massimo", Num: true}}}
	for _, row := range []struct{ class, label string }{
		{"delivery", "Consegne riuscite (smtp/lmtp)"},
		{"filter", "Passaggio al content filter"},
	} {
		d, err := g.Q.Delays(lo, hi, row.class)
		if err != nil {
			return Block{}, err
		}
		if d.Count == 0 {
			tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: row.label}, {Text: "0", Num: true},
				{Text: "—", Num: true}, {Text: "—", Num: true}, {Text: "—", Num: true}, {Text: "—", Num: true}, {Text: "—", Num: true}, {Text: "—", Num: true}}})
			continue
		}
		tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: row.label}, {Text: fmtInt(float64(d.Count)), Num: true},
			{Text: fmtSeconds(d.Avg), Num: true}, {Text: fmtSeconds(d.P50), Num: true}, {Text: fmtSeconds(d.P90), Num: true},
			{Text: fmtSeconds(d.P95), Num: true}, {Text: fmtSeconds(d.P99), Num: true}, {Text: fmtSeconds(d.Max), Num: true}}})
	}
	return Block{Kind: "card", Title: "Tempi di consegna",
		Note:     "Campo delay= di Postfix, per destinatario consegnato; percentili esatti (nearest-rank) sulle consegne del periodo.",
		Children: []Block{{Kind: "table", Table: tbl}}}, nil
}

// delayTrend charts average and p95 delay for each sub-bucket.
func (g *Generator) delayTrend(id string, buckets []query.Bucket, labels, short, links []string) (Block, error) {
	vals := make([]query.Metrics, len(buckets))
	for i, b := range buckets {
		d, err := g.Q.Delays(b.Start, b.End, "delivery")
		if err != nil {
			return Block{}, err
		}
		vals[i] = query.Metrics{"avg": d.Avg, "p95": d.P95, "p50": d.P50}
		if d.Count == 0 && b.Status != query.Missing {
			vals[i] = query.Metrics{}
		}
	}
	return g.bucketChart(id, "Ritardi di consegna", "line", "s", buckets, labels, short, links, vals, []seriesDef{
		{"Ritardo medio", "avg", colAccepted}, {"Mediana (p50)", "p50", colSent}, {"95° percentile", "p95", colDeferred},
	}), nil
}

// ---- security / TLS / SASL ----

func (g *Generator) securityCard(t query.Metrics) Block {
	sess := t.Get(ag.MSessions)
	tlsPct := "n/d"
	if sess > 0 {
		tlsPct = fmtPct(t.Get(ag.MSessionsTLS) / sess * 100)
	}
	rows := []Tr{
		row2("Connessioni SMTP in ingresso", fmtInt(t.Get(ag.MConnections)), "connessioni"),
		row2("Sessioni concluse", fmtInt(sess), "sessioni"),
		row2("Sessioni con STARTTLS", fmtInt(t.Get(ag.MSessionsTLS))+" ("+tlsPct+")", "sessioni"),
		row2("Messaggi con autenticazione SASL", fmtInt(t.Get(ag.MSASLOK)), "messaggi accettati"),
		row2("Autenticazioni SASL fallite", fmtInt(t.Get(ag.MSASLFail)), "eventi"),
		row2("Errori TLS", fmtInt(t.Get(ag.MTLSError)), "eventi"),
		row2("Sessioni interrotte (lost connection / timeout)", fmtInt(t.Get(ag.MSessionAbort)), "eventi"),
		row2("Handshake TLS registrati in ingresso / uscita", fmtInt(t.Get(ag.MTLSIn))+" / "+fmtInt(t.Get(ag.MTLSOut)), "connessioni"),
	}
	note := "STARTTLS dalle statistiche di fine sessione di smtpd."
	if t.Get(ag.MTLSIn)+t.Get(ag.MTLSOut) == 0 {
		note += " Nessun handshake TLS registrato (tls_loglevel = 0): TLS in uscita non misurabile."
	}
	return Block{Kind: "card", Title: "TLS e SASL", Note: note, Children: []Block{{Kind: "table", Table: &Table{
		ID: "security", Head: []Th{{Text: "Indicatore"}, {Text: "Valore", Num: true}, {Text: "Unità"}}, Rows: rows}}}}
}

func row2(a, b, c string) Tr {
	return Tr{Cells: []Td{{Text: a}, {Text: b, Num: true}, {Text: c, Title: c}}}
}

func (g *Generator) compositionCard(days []string) (Block, error) {
	if len(days) == 0 {
		return Block{}, nil
	}
	tbl := &Table{ID: "composition", Sortable: true, Head: []Th{{Text: "Categoria"}, {Text: "Voce"}, {Text: "Conteggio", Num: true}}}
	for _, d := range []struct{ dim, label string }{{ag.DOrigin, "Istanze di coda per origine"}, {ag.DAgent, "Righe di stato per agente"}} {
		items, _, _, err := g.Q.Ranking(days[0], days[len(days)-1], d.dim, 100)
		if err != nil {
			return Block{}, err
		}
		for _, it := range items {
			tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: d.label}, {Text: it.Key, Key: true},
				{Text: fmtInt(float64(it.Value)), Num: true, Sort: fmt.Sprint(it.Value)}}})
		}
	}
	return Block{Kind: "card", Title: "Composizione del traffico",
		Note:     "Le istanze 'reinject' sono il secondo passaggio dei messaggi dopo il content filter e non sono conteggiate come nuovi messaggi.",
		Children: []Block{{Kind: "table", Table: tbl}}}, nil
}

// ---- comparison ----

var comparedMetrics = []struct{ label, metric, unit string }{
	{"Messaggi accettati", ag.MAccepted, "messaggi"},
	{"Destinatari", ag.MRecipients, "destinatari"},
	{"Volume", ag.MBytes, "bytes"},
	{"Consegne riuscite", ag.MSent, "destinatari"},
	{"Tentativi differiti", ag.MDeferred, "tentativi"},
	{"Fallimenti definitivi", ag.MFinalFailures, "destinatari"},
	{"Rifiuti SMTP", ag.MRejects, "eventi"},
	{"Autenticazioni SASL fallite", ag.MSASLFail, "eventi"},
}

func (g *Generator) comparison(cur *periodData, prev *periodData, prevLabel string) Block {
	comparable := cur.cov.Status == query.Complete && prev.cov.Status == query.Complete
	tbl := &Table{ID: "comparison", Head: []Th{{Text: "Metrica"}, {Text: "Unità"}, {Text: "Periodo", Num: true},
		{Text: prevLabel, Num: true}, {Text: "Variazione", Num: true}}}
	for _, m := range comparedMetrics {
		c, p := cur.totals.Get(m.metric), prev.totals.Get(m.metric)
		pv := fmtUnit(p, m.unit)
		d, _ := fmtDelta(c, p)
		if prev.cov.Status == query.Missing {
			pv, d = "n/d", "n/d"
		} else if !comparable {
			d += " *"
		}
		unit := m.unit
		if unit == "bytes" {
			unit = "byte"
		}
		tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: m.label}, {Text: unit}, {Text: fmtUnit(c, m.unit), Num: true},
			{Text: pv, Num: true}, {Text: d, Num: true}}})
	}
	note := "Variazioni calcolate sui periodi interamente osservati."
	if !comparable {
		note = fmt.Sprintf("* Copertura diversa: periodo %s, precedente %s. Le variazioni non sono confrontabili.",
			fmtPct(cur.cov.Fraction*100), fmtPct(prev.cov.Fraction*100))
	}
	return Block{Kind: "card", Title: "Confronto con " + prevLabel, Note: note, Children: []Block{{Kind: "table", Table: tbl}}}
}

// ---- pages ----

func section(title, note string, children ...Block) Block {
	return Block{Kind: "section", Title: title, Note: note, Children: children}
}

func grid(children ...Block) Block {
	var c []Block
	for _, b := range children {
		if b.Kind != "" {
			c = append(c, b)
		}
	}
	return Block{Kind: "grid", Children: c}
}

func card(title, note string, children ...Block) Block {
	return Block{Kind: "card", Title: title, Note: note, Children: children}
}

func (g *Generator) periodPage(p period.Period) (*Page, error) {
	pd, err := g.load(p)
	if err != nil {
		return nil, err
	}
	root := "../"
	pg := g.basePage(root, g.title(p), string(p.Kind), p.Start)
	pg.Crumbs = g.crumbs(root, p)
	pg.Prev = g.link(root, p.Prev(), g.shortTitle(p.Prev()))
	pg.Next = g.link(root, p.Next(), g.shortTitle(p.Next()))
	if p.Kind != period.Year {
		up := period.Of(map[period.Kind]period.Kind{period.Day: period.Month, period.Week: period.Month, period.Month: period.Year}[p.Kind], p.Start, g.loc())
		pg.Up = g.link(root, up, g.shortTitle(up))
	}
	pg.Subtitle = fmt.Sprintf("Copertura: %s · %s", statusText(pd.cov), g.observedSummary(pd))
	pg.Banners = g.coverageBanners(pd)
	if pd.cov.Status == query.Missing {
		pg.Blocks = append(pg.Blocks, section("Copertura", "Nessun dato da mostrare.", g.coverageTable(pd)))
		return pg, nil
	}
	switch p.Kind {
	case period.Day:
		err = g.dayBlocks(pg, pd)
	case period.Week:
		err = g.weekBlocks(pg, pd)
	case period.Month:
		err = g.monthBlocks(pg, pd)
	case period.Year:
		err = g.yearBlocks(pg, pd)
	}
	return pg, err
}

func (g *Generator) observedSummary(pd *periodData) string {
	n := 0
	for _, b := range pd.children {
		if b.Status != query.Missing && b.Status != "nonexistent" {
			n++
		}
	}
	unit := map[period.Kind]string{period.Day: "ore con dati", period.Week: "giorni con dati", period.Month: "giorni con dati", period.Year: "mesi con dati"}[pd.p.Kind]
	return fmt.Sprintf("%d/%d %s", n, len(pd.children), unit)
}

func (g *Generator) crumbs(root string, p period.Period) []Link {
	c := []Link{{Href: root + "index.html", Text: "Panoramica"}}
	if p.Kind == period.Year {
		return c
	}
	y := period.Of(period.Year, p.Start, g.loc())
	if l := g.link(root, y, y.Key); l != nil {
		c = append(c, *l)
	}
	if p.Kind == period.Day {
		m := period.Of(period.Month, p.Start, g.loc())
		if l := g.link(root, m, g.shortTitle(m)); l != nil {
			c = append(c, *l)
		}
		w := period.Of(period.Week, p.Start, g.loc())
		if l := g.link(root, w, w.Key); l != nil {
			c = append(c, *l)
		}
	}
	return c
}

func (g *Generator) coverageTable(pd *periodData) Block {
	tbl := &Table{ID: "coverage", Head: []Th{{Text: "Intervallo"}, {Text: "Copertura"}, {Text: "Osservato", Num: true}}}
	for _, b := range pd.children {
		tbl.Rows = append(tbl.Rows, Tr{Class: b.Status, Cells: []Td{{Text: g.bucketLabel(pd.p.Kind, b)},
			{Text: statusText(b), Badge: badgeClass(b.Status)}, {Text: fmtSeconds(float64(b.Covered)), Num: true}}})
	}
	return card("Copertura per intervallo", "Un intervallo senza righe di log non è traffico zero: è un dato assente.", Block{Kind: "table", Table: tbl})
}

func (g *Generator) bucketLabel(k period.Kind, b query.Bucket) string {
	switch k {
	case period.Day:
		return b.Key + ":00–" + b.Key + ":59"
	case period.Year:
		t := time.Unix(b.Start, 0).In(g.loc())
		return monthNames[t.Month()-1]
	default:
		t := time.Unix(b.Start, 0).In(g.loc())
		return weekdayNames[weekdayIndex(t)] + " " + t.Format("02/01")
	}
}

func (g *Generator) dayBlocks(pg *Page, pd *periodData) error {
	byHour, err := g.Q.ByHour(pd.days)
	if err != nil {
		return err
	}
	vals := byHour[:]
	labels := make([]string, len(pd.children))
	for i, b := range pd.children {
		labels[i] = b.Key + ":00"
	}
	prev, err := g.load(pd.p.Prev())
	if err != nil {
		return err
	}
	pg.Blocks = append(pg.Blocks,
		section("Indicatori del giorno", "Variazione rispetto al giorno precedente, solo se entrambi i giorni sono interamente osservati.",
			g.kpis(pd.totals, prev.totals, pd.cov.Status == query.Complete && prev.cov.Status == query.Complete)),
		section("Traffico orario", "Ore locali 00–23. Le ore senza dati sono tratteggiate e non valgono zero.",
			card("", "", g.bucketChart("hourly", "Traffico orario", "bar", "", pd.children, labels, hourShort(pd.children), nil, vals, trafficSeries))),
	)
	delays, err := g.delayTable("delays", pd.p.Start.Unix(), pd.p.End.Unix())
	if err != nil {
		return err
	}
	vol := g.bucketChart("volume", "Volume orario", "bar", "bytes", pd.children, labels, hourShort(pd.children), nil, vals,
		[]seriesDef{{"Volume accettato", ag.MBytes, colBytes}})
	pg.Blocks = append(pg.Blocks, section("Volume e prestazioni", "", grid(card("Volume orario (byte accettati)", "", vol), delays)))
	return g.commonTail(pg, pd)
}

func hourShort(b []query.Bucket) []string {
	s := make([]string, len(b))
	for i, x := range b {
		s[i] = x.Key
	}
	return s
}

// commonTail appends rankings, security and composition sections.
func (g *Generator) commonTail(pg *Page, pd *periodData) error {
	ranks, err := g.rankingCards(string(pd.p.Kind), pd.days, rankings)
	if err != nil {
		return err
	}
	comp, err := g.compositionCard(pd.days)
	if err != nil {
		return err
	}
	pg.Blocks = append(pg.Blocks,
		section("Classifiche", fmt.Sprintf("Primi %d valori per categoria; tabelle ordinabili e filtrabili.", g.Cfg.Reports.TopLimit), grid(ranks...)),
		section("Sicurezza e composizione", "", grid(g.securityCard(pd.totals), comp)),
		section("Copertura", "", g.coverageTable(pd)),
	)
	return nil
}

func (g *Generator) dayLinks(root string, buckets []query.Bucket) []string {
	out := make([]string, len(buckets))
	for i, b := range buckets {
		out[i] = g.href(root, period.Of(period.Day, time.Unix(b.Start, 0), g.loc()))
	}
	return out
}

func (g *Generator) dayLabels(buckets []query.Bucket) ([]string, []string) {
	l, s := make([]string, len(buckets)), make([]string, len(buckets))
	for i, b := range buckets {
		t := time.Unix(b.Start, 0).In(g.loc())
		l[i] = weekdayNames[weekdayIndex(t)] + " " + t.Format("02/01/2006")
		s[i] = t.Format("02")
	}
	return l, s
}

func (g *Generator) dailyValues(pd *periodData) ([]query.Metrics, error) {
	byDay, err := g.Q.ByDay(pd.days[0], pd.days[len(pd.days)-1])
	if err != nil {
		return nil, err
	}
	vals := make([]query.Metrics, len(pd.children))
	for i, b := range pd.children {
		vals[i] = byDay[b.Key]
		if vals[i] == nil {
			vals[i] = query.Metrics{}
		}
	}
	return vals, nil
}

func (g *Generator) weekBlocks(pg *Page, pd *periodData) error {
	vals, err := g.dailyValues(pd)
	if err != nil {
		return err
	}
	labels, short := g.dayLabels(pd.children)
	for i, b := range pd.children {
		short[i] = weekdayNames[weekdayIndex(time.Unix(b.Start, 0).In(g.loc()))][:3] + " " + short[i]
	}
	links := g.dayLinks("../", pd.children)
	prev, err := g.load(pd.p.Prev())
	if err != nil {
		return err
	}
	comparable := pd.cov.Status == query.Complete && prev.cov.Status == query.Complete
	byHour, err := g.Q.ByHour(pd.days)
	if err != nil {
		return err
	}
	hb := aggregatedHourBuckets(pd)
	hl := make([]string, 24)
	for i := range hl {
		hl[i] = twoDigit(i) + ":00"
	}
	trend, err := g.delayTrend("delaytrend", pd.children, labels, short, links)
	if err != nil {
		return err
	}
	delays, err := g.delayTable("delays", pd.p.Start.Unix(), pd.p.End.Unix())
	if err != nil {
		return err
	}
	pg.Blocks = append(pg.Blocks,
		section("Indicatori della settimana", "Variazione rispetto alla settimana precedente, solo se entrambe sono interamente osservate.", g.kpis(pd.totals, prev.totals, comparable)),
		section("Traffico giornaliero", "Da lunedì a domenica (settimana ISO 8601). Clic su un giorno per aprire il report giornaliero.",
			card("", "", g.bucketChart("daily", "Traffico giornaliero", "bar", "", pd.children, labels, short, links, vals, trafficSeries))),
		section("Distribuzione oraria aggregata", "Somma, per ora del giorno, dei giorni osservati della settimana.",
			card("", "", g.bucketChart("hourdist", "Distribuzione oraria", "bar", "", hb, hl, hourShort(hb), nil, byHour[:], trafficSeries))),
		section("Confronto e ritardi", "", grid(g.comparison(pd, prev, "settimana "+prev.p.Key), delays)),
		section("Andamento dei ritardi", "", card("", "", trend)),
	)
	return g.commonTail(pg, pd)
}

// aggregatedHourBuckets returns 24 buckets whose status summarizes the
// coverage of that hour across the days of the period.
func aggregatedHourBuckets(pd *periodData) []query.Bucket {
	out := make([]query.Bucket, 24)
	for h := range out {
		out[h] = query.Bucket{Key: twoDigit(h), Status: query.Complete, Fraction: 1}
	}
	anyObserved := false
	for _, b := range pd.children {
		if b.Status != query.Missing {
			anyObserved = true
		}
	}
	if !anyObserved {
		for h := range out {
			out[h].Status = query.Missing
		}
	} else if pd.cov.Status != query.Complete {
		for h := range out {
			out[h].Status, out[h].Fraction = query.Partial, pd.cov.Fraction
		}
	}
	return out
}

func twoDigit(i int) string { return fmt.Sprintf("%02d", i) }

func (g *Generator) monthBlocks(pg *Page, pd *periodData) error {
	vals, err := g.dailyValues(pd)
	if err != nil {
		return err
	}
	labels, short := g.dayLabels(pd.children)
	links := g.dayLinks("../", pd.children)
	prev, err := g.load(pd.p.Prev())
	if err != nil {
		return err
	}
	comparable := pd.cov.Status == query.Complete && prev.cov.Status == query.Complete
	delays, err := g.delayTable("delays", pd.p.Start.Unix(), pd.p.End.Unix())
	if err != nil {
		return err
	}
	trend, err := g.delayTrend("delaytrend", pd.children, labels, short, links)
	if err != nil {
		return err
	}
	pg.Blocks = append(pg.Blocks,
		section("Indicatori del mese", "Variazione rispetto al mese precedente, solo se entrambi sono interamente osservati.", g.kpis(pd.totals, prev.totals, comparable)),
		section("Traffico per giorno", "Clic su un giorno per aprire il report giornaliero.",
			card("", "", g.bucketChart("daily", "Traffico per giorno", "bar", "", pd.children, labels, short, links, vals, trafficSeries))),
		section("Confronto e tempi di consegna", "", grid(g.comparison(pd, prev, monthNames[prev.p.Start.Month()-1]+" "+prev.p.Start.Format("2006")), delays)),
		section("Andamento dei ritardi", "", card("", "", trend)),
		section("Giorni e giorni della settimana", "", grid(g.topDays(pd, vals, labels, links), g.weekdayTable(pd, vals))),
	)
	return g.commonTail(pg, pd)
}

func (g *Generator) topDays(pd *periodData, vals []query.Metrics, labels, links []string) Block {
	type dv struct {
		i int
		v float64
	}
	var ds []dv
	for i, b := range pd.children {
		if b.Status != query.Missing {
			ds = append(ds, dv{i, vals[i].Get(ag.MAccepted)})
		}
	}
	sort.SliceStable(ds, func(a, b int) bool { return ds[a].v > ds[b].v })
	if len(ds) > 10 {
		ds = ds[:10]
	}
	tbl := &Table{ID: "topdays", Sortable: true, Head: []Th{{Text: "Giorno"}, {Text: "Copertura"}, {Text: "Messaggi accettati", Num: true},
		{Text: "Consegne riuscite", Num: true}, {Text: "Rifiuti", Num: true}}}
	for _, d := range ds {
		b := pd.children[d.i]
		tbl.Rows = append(tbl.Rows, Tr{Class: b.Status, Cells: []Td{{Text: labels[d.i], Href: links[d.i]},
			{Text: statusText(b), Badge: badgeClass(b.Status)},
			{Text: fmtInt(d.v), Num: true, Sort: fmt.Sprint(d.v)},
			{Text: fmtInt(vals[d.i].Get(ag.MSent)), Num: true, Sort: fmt.Sprint(vals[d.i].Get(ag.MSent))},
			{Text: fmtInt(vals[d.i].Get(ag.MRejects)), Num: true, Sort: fmt.Sprint(vals[d.i].Get(ag.MRejects))}}})
	}
	return card("Giorni con maggiore traffico", "Ordinati per messaggi accettati; solo giorni con dati.", Block{Kind: "table", Table: tbl})
}

func (g *Generator) weekdayTable(pd *periodData, vals []query.Metrics) Block {
	var sum [7]query.Metrics
	var n [7]int
	var partial [7]bool
	for i := range sum {
		sum[i] = query.Metrics{}
	}
	for i, b := range pd.children {
		if b.Status == query.Missing {
			continue
		}
		w := weekdayIndex(time.Unix(b.Start, 0).In(g.loc()))
		n[w]++
		if b.Status == query.Partial {
			partial[w] = true
		}
		for k, v := range vals[i] {
			sum[w][k] += v
		}
	}
	tbl := &Table{ID: "weekdays", Sortable: true, Head: []Th{{Text: "Giorno della settimana"}, {Text: "Giorni con dati", Num: true},
		{Text: "Messaggi accettati", Num: true}, {Text: "Media per giorno", Num: true}, {Text: "Consegne riuscite", Num: true}, {Text: "Rifiuti", Num: true}}}
	for w := 0; w < 7; w++ {
		avg := "n/d"
		if n[w] > 0 {
			avg = fmtDec(sum[w].Get(ag.MAccepted)/float64(n[w]), 1)
			if partial[w] {
				avg += " *"
			}
		}
		tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: weekdayNames[w], Sort: fmt.Sprint(w)}, {Text: fmt.Sprint(n[w]), Num: true, Sort: fmt.Sprint(n[w])},
			{Text: fmtInt(sum[w].Get(ag.MAccepted)), Num: true, Sort: fmt.Sprint(sum[w].Get(ag.MAccepted))},
			{Text: avg, Num: true}, {Text: fmtInt(sum[w].Get(ag.MSent)), Num: true, Sort: fmt.Sprint(sum[w].Get(ag.MSent))},
			{Text: fmtInt(sum[w].Get(ag.MRejects)), Num: true, Sort: fmt.Sprint(sum[w].Get(ag.MRejects))}}})
	}
	return card("Distribuzione per giorno della settimana", "Media sui soli giorni con dati; * = include giorni a copertura parziale.", Block{Kind: "table", Table: tbl})
}

func (g *Generator) yearBlocks(pg *Page, pd *periodData) error {
	byDay, err := g.Q.ByDay(pd.days[0], pd.days[len(pd.days)-1])
	if err != nil {
		return err
	}
	vals := make([]query.Metrics, len(pd.children))
	labels := make([]string, len(pd.children))
	short := make([]string, len(pd.children))
	links := make([]string, len(pd.children))
	for i, b := range pd.children {
		m := period.Of(period.Month, time.Unix(b.Start, 0), g.loc())
		vals[i] = query.Metrics{}
		for _, d := range m.Days() {
			for k, v := range byDay[d] {
				vals[i][k] += v
			}
		}
		labels[i] = monthNames[m.Start.Month()-1] + " " + m.Start.Format("2006")
		short[i] = monthShort[m.Start.Month()-1]
		links[i] = g.href("../", m)
	}
	prev, err := g.load(pd.p.Prev())
	if err != nil {
		return err
	}
	comparable := pd.cov.Status == query.Complete && prev.cov.Status == query.Complete
	delays, err := g.delayTable("delays", pd.p.Start.Unix(), pd.p.End.Unix())
	if err != nil {
		return err
	}
	trend, err := g.delayTrend("delaytrend", pd.children, labels, short, links)
	if err != nil {
		return err
	}
	summary := &Table{ID: "months", Sortable: true, Head: []Th{{Text: "Mese"}, {Text: "Copertura"}, {Text: "Messaggi", Num: true},
		{Text: "Destinatari", Num: true}, {Text: "Volume", Num: true}, {Text: "Consegne", Num: true}, {Text: "Differiti", Num: true},
		{Text: "Fallimenti", Num: true}, {Text: "Rifiuti", Num: true}}}
	for i, b := range pd.children {
		r := Tr{Class: b.Status, Cells: []Td{{Text: labels[i], Href: links[i], Sort: fmt.Sprint(i)}, {Text: statusText(b), Badge: badgeClass(b.Status)}}}
		for _, m := range []struct{ k, u string }{{ag.MAccepted, ""}, {ag.MRecipients, ""}, {ag.MBytes, "bytes"}, {ag.MSent, ""},
			{ag.MDeferred, ""}, {ag.MFinalFailures, ""}, {ag.MRejects, ""}} {
			if b.Status == query.Missing {
				r.Cells = append(r.Cells, Td{Text: "n/d", Num: true, Sort: ""})
				continue
			}
			v := vals[i].Get(m.k)
			r.Cells = append(r.Cells, Td{Text: fmtUnit(v, m.u), Num: true, Sort: fmt.Sprint(v)})
		}
		summary.Rows = append(summary.Rows, r)
	}
	pg.Blocks = append(pg.Blocks,
		section("Indicatori dell'anno", "Variazione rispetto all'anno precedente, solo se entrambi sono interamente osservati.", g.kpis(pd.totals, prev.totals, comparable)),
		section("Messaggi per mese", "Clic su un mese per aprire il report mensile.",
			card("", "", g.bucketChart("monthly", "Traffico mensile", "bar", "", pd.children, labels, short, links, vals, trafficSeries))),
		section("Andamento di rifiuti, differimenti e fallimenti", "",
			card("", "", g.bucketChart("trend", "Anomalie mensili", "line", "", pd.children, labels, short, links, vals, trafficSeries[2:]))),
		section("Volume mensile", "", card("", "", g.bucketChart("volume", "Volume mensile", "bar", "bytes", pd.children, labels, short, links, vals,
			[]seriesDef{{"Volume accettato", ag.MBytes, colBytes}}))),
		section("Confronto e tempi di consegna", "", grid(g.comparison(pd, prev, "anno "+prev.p.Key), delays)),
		section("Tempi medi e percentili per mese", "", card("", "", trend)),
		section("Riepilogo dei dodici mesi", "", card("", "", Block{Kind: "table", Table: summary})),
	)
	return g.commonTail(pg, pd)
}

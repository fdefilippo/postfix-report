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

// weekdayIndex returns 0 for Monday … 6 for Sunday.
func weekdayIndex(t time.Time) int { return (int(t.Weekday()) + 6) % 7 }

func (g *Generator) title(p period.Period) string {
	switch p.Kind {
	case period.Day:
		return fmt.Sprintf("%s %d %s %d", g.weekday(p.Start), p.Start.Day(), g.monthName(p.Start.Month()), p.Start.Year())
	case period.Week:
		last := p.End.Add(-time.Hour)
		if g.l().lang == "en" {
			return fmt.Sprintf("Week %s (%s – %s)", p.Key, p.Start.Format("Jan 2"), last.Format("Jan 2, 2006"))
		}
		return fmt.Sprintf("Settimana %s (%d/%02d – %d/%02d/%d)", p.Key, p.Start.Day(), int(p.Start.Month()),
			last.Day(), int(last.Month()), last.Year())
	case period.Month:
		return capitalize(g.monthName(p.Start.Month())) + " " + p.Start.Format("2006")
	default:
		return g.trf("Anno %s", p.Key)
	}
}

func (g *Generator) shortTitle(p period.Period) string {
	switch p.Kind {
	case period.Day:
		return p.Start.Format(g.l().dateFmt)
	case period.Month:
		return g.monthShort(p.Start.Month()) + " " + p.Start.Format("2006")
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
// period page every kind stays listed: the page itself and the coarser periods
// containing it, and for the finer kinds the latest of them inside the page
// (its last day with data). Links therefore depend only on the page's own
// period and the data range, so pages which are not regenerated never carry
// stale "latest" links.
func (g *Generator) nav(root string, active string, p period.Period) []NavItem {
	items := []NavItem{{Href: root + "index.html", Text: g.tr("Panoramica"), Active: active == "index"}}
	if !g.hasData {
		return items
	}
	last := p.End.Add(-time.Second)
	if g.lastDay.Start.Before(last) {
		last = g.lastDay.Start
	}
	for _, k := range period.Kinds {
		if !g.enabled(k) {
			continue
		}
		at := p.Start
		if active == "index" || indexOf(string(k)) < indexOf(active) {
			at = last
		}
		q := period.Of(k, at, g.loc())
		if h := g.href(root, q); h != "" {
			items = append(items, NavItem{Href: h, Text: g.tr(kindNames[k]), Active: active == string(k)})
		}
	}
	return items
}

// kindNames names the report kinds (Italian source text, see tr).
var kindNames = map[period.Kind]string{period.Day: "Giornaliero", period.Week: "Settimanale", period.Month: "Mensile", period.Year: "Annuale"}

// capitalize upper-cases the first letter of s.
func capitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func indexOf(kind string) int {
	for i, k := range period.Kinds {
		if string(k) == kind {
			return i
		}
	}
	return 0
}

func (g *Generator) basePage(root, title, active string, p period.Period) *Page {
	return &Page{
		Lang: g.l().lang, Root: root, SiteTitle: g.Cfg.Output.Title, Title: title, Nav: g.nav(root, active, p),
		Generated: generatedPlaceholder, Version: g.Version, AssetVer: assetVer, Privacy: g.privacyNote(),
	}
}

func (g *Generator) privacyNote() string {
	var p []string
	if g.Cfg.Privacy.AnonymizeEmail {
		p = append(p, g.tr("indirizzi e utenti pseudonimizzati"))
	}
	if g.Cfg.Privacy.AnonymizeIP {
		p = append(p, g.tr("IP troncati"))
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
		return []Banner{{"bad", g.tr("Dati assenti: nessuna riga di log acquisita per questo periodo. I valori non sono zero: non sono disponibili.")}}
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
		unit := map[period.Kind]string{period.Day: "Ore", period.Week: "Giorni", period.Month: "Giorni", period.Year: "Mesi"}[pd.p.Kind]
		obs := g.observedRange(pd.p)
		return []Banner{{"warn", g.trf("Copertura parziale: osservato il %s del periodo (%s). %s: %d con copertura completa, %d parziale, %d senza dati. I totali si riferiscono solo all'intervallo osservato; le variazioni rispetto al periodo precedente non sono confrontabili.",
			g.fmtPct(pd.cov.Fraction*100), obs, g.tr(unit), full, part, miss)}}
	}
	return nil
}

func (g *Generator) observedRange(p period.Period) string {
	ivs, err := g.Q.Intervals(p.Start.Unix(), p.End.Unix())
	if err != nil || len(ivs) == 0 {
		return g.tr("nessun intervallo")
	}
	var parts []string
	for i, iv := range ivs {
		if i == 4 {
			parts = append(parts, g.trf("e altri %d intervalli", len(ivs)-4))
			break
		}
		s, e := max(iv.Start, p.Start.Unix()), min(iv.End, p.End.Unix()-1)
		parts = append(parts, g.trf("dal %s al %s", g.ts(s), g.ts(e)))
	}
	return strings.Join(parts, "; ")
}

func (g *Generator) ts(u int64) string {
	return time.Unix(u, 0).In(g.loc()).Format(g.l().tsFmt)
}

// ---- KPIs ----

func (g *Generator) kpis(t query.Metrics, prev query.Metrics, comparable bool) Block {
	delta := func(k string) (string, string) {
		if prev == nil {
			return "", ""
		}
		if !comparable {
			return g.tr("variazione non confrontabile"), "na"
		}
		d, c := g.fmtDelta(t.Get(k), prev.Get(k))
		return g.trf("%s vs periodo prec.", d), c
	}
	mk := func(label, metric, unit, class string, value string) KPI {
		k := KPI{Label: g.tr(label), Value: value, Unit: unit, Class: class}
		if metric != "" {
			k.Delta, k.DeltaClass = delta(metric)
		}
		return k
	}
	acc := t.Get(ag.MAccepted)
	avgSize, avgDelay := g.tr("n/d"), g.tr("n/d")
	if acc > 0 {
		avgSize = g.fmtBytes(t.Get(ag.MBytes) / acc)
	}
	if n := t.Get(ag.MDelayCount); n > 0 {
		avgDelay = g.fmtSeconds(t.Get(ag.MDelaySum) / n)
	}
	ks := []KPI{
		mk("Messaggi accettati", ag.MAccepted, g.trf("messaggi · SMTP %s · locali %s", g.fmtInt(t.Get(ag.MAcceptedSMTP)), g.fmtInt(t.Get(ag.MAcceptedLocal))), "accepted", g.fmtInt(acc)),
		mk("Destinatari", ag.MRecipients, g.tr("destinatari dei messaggi accettati"), "accepted", g.fmtInt(t.Get(ag.MRecipients))),
		mk("Consegne riuscite", ag.MSent, g.trf("destinatari consegnati · di cui notifiche %s", g.fmtInt(t.Get(ag.MSentNotif))), "sent", g.fmtInt(t.Get(ag.MSent))),
		mk("Differimenti", ag.MDeferred, g.trf("tentativi differiti · poi consegnati %s", g.fmtInt(t.Get(ag.MSentAfterDefer))), "deferred", g.fmtInt(t.Get(ag.MDeferred))),
		mk("Fallimenti definitivi", ag.MFinalFailures, g.trf("destinatari · bounce %s · scaduti %s", g.fmtInt(t.Get(ag.MBounced)), g.fmtInt(t.Get(ag.MExpiredRcpt))), "failed", g.fmtInt(t.Get(ag.MFinalFailures))),
		mk("Rifiuti SMTP", ag.MRejects, g.trf("eventi · temporanei %s · permanenti %s", g.fmtInt(t.Get(ag.MRejectsTemp)), g.fmtInt(t.Get(ag.MRejectsPerm))), "rejected", g.fmtInt(t.Get(ag.MRejects))),
		mk("Volume", ag.MBytes, g.tr("byte dei messaggi accettati"), "", g.fmtBytes(t.Get(ag.MBytes))),
		mk("Dimensione media", "", g.tr("per messaggio accettato"), "", avgSize),
		mk("Ritardo medio", "", g.tr("per consegna riuscita (campo delay)"), "", avgDelay),
		mk("Notifiche generate", ag.MNotifications, g.tr("messaggi bounce/DSN creati da Postfix"), "", g.fmtInt(t.Get(ag.MNotifications))),
	}
	if d := t.Get(ag.MDiscarded); d > 0 {
		ks = append(ks, mk("Messaggi scartati", ag.MDiscarded, g.tr("azioni discard"), "", g.fmtInt(d)))
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

// weekends marks the Saturdays and Sundays of a chart over days, so that the
// chart can tell them apart from working days; nil for any other bucket
// size (hours, months) or when no bucket falls on a weekend.
func (g *Generator) weekends(buckets []query.Bucket) []bool {
	var out []bool
	found := false
	for _, b := range buckets {
		// A local day lasts 23 to 25 hours across DST changes.
		if d := b.End - b.Start; d < 23*3600 || d > 25*3600 {
			return nil
		}
		w := time.Unix(b.Start, 0).In(g.loc()).Weekday()
		we := w == time.Saturday || w == time.Sunday
		found = found || we
		out = append(out, we)
	}
	if !found {
		return nil
	}
	return out
}

// bucketChart builds a chart over buckets; values of buckets without data are null.
func (g *Generator) bucketChart(id, title, typ, unit string, buckets []query.Bucket, labels, short []string,
	links []string, values []query.Metrics, defs []seriesDef) Block {
	spec := chartSpec{Type: typ, Title: g.tr(title), Labels: labels, Short: short, Unit: unit, Weekend: g.weekends(buckets)}
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
	head := []Th{{Text: g.tr("Periodo")}, {Text: g.tr("Copertura")}}
	for _, d := range defs {
		head = append(head, Th{Text: g.tr(d.name), Num: true})
	}
	tbl := &Table{ID: id + "-table", Head: head}
	for _, d := range defs {
		s := seriesSpec{Name: g.tr(d.name), Color: d.color}
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
		row.Cells = append(row.Cells, cell, Td{Text: g.statusText(b), Badge: badgeClass(b.Status)})
		for _, d := range defs {
			if b.Status == query.Missing || b.Status == "nonexistent" {
				row.Cells = append(row.Cells, Td{Text: g.tr("n/d"), Num: true})
				continue
			}
			v := values[i].Get(d.metric)
			row.Cells = append(row.Cells, Td{Text: g.fmtUnit(v, unit), Num: true, Sort: fmt.Sprint(v)})
		}
		tbl.Rows = append(tbl.Rows, row)
	}
	return Block{Kind: "chart", Chart: &Chart{ID: id, JSON: toJS(spec), Table: tbl}}
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

func (g *Generator) fmtUnit(v float64, unit string) string {
	switch unit {
	case "bytes":
		return g.fmtBytes(v)
	case "s":
		return g.fmtSeconds(v)
	}
	if v != float64(int64(v)) {
		return g.fmtDec(v, 2)
	}
	return g.fmtInt(v)
}

func (g *Generator) statusText(b query.Bucket) string {
	switch b.Status {
	case query.Complete:
		return g.tr("completa")
	case query.Partial:
		return g.trf("parziale %s", g.fmtPct(b.Fraction*100))
	case "nonexistent":
		return g.tr("ora inesistente")
	default:
		return g.tr("dati assenti")
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
			Head:  []Th{{Text: "#", Num: true}, {Text: g.tr("Valore")}, {Text: g.tr(d.unit), Num: true}, {Text: "%", Num: true}},
			Empty: g.tr("Nessun evento nel periodo.")}
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
				{Text: g.fmtInt(float64(it.Value)), Num: true, Sort: fmt.Sprint(it.Value), Bar: float64(it.Value) / float64(maxV)},
				{Text: g.fmtPct(pct), Num: true, Sort: fmt.Sprintf("%.4f", pct)},
			}})
		}
		note := g.trf("Unità: %s. Totale %s, %d valori distinti", g.tr(d.unit), g.fmtInt(float64(total)), distinct)
		if distinct > len(items) {
			note += g.trf(", mostrati i primi %d", len(items))
		}
		cards = append(cards, Block{Kind: "card", Title: g.tr(d.title), Note: note + ".", Children: []Block{{Kind: "table", Table: tbl}}})
	}
	return cards, nil
}

// ---- delays ----

func (g *Generator) delayTable(id string, lo, hi int64) (Block, error) {
	tbl := &Table{ID: id, Head: []Th{{Text: g.tr("Tipo")}, {Text: g.tr("Consegne"), Num: true}, {Text: g.tr("Media"), Num: true},
		{Text: "p50", Num: true}, {Text: "p90", Num: true}, {Text: "p95", Num: true}, {Text: "p99", Num: true}, {Text: g.tr("Massimo"), Num: true}}}
	for _, row := range []struct{ class, label string }{
		{"delivery", "Consegne riuscite (smtp/lmtp)"},
		{"filter", "Passaggio al content filter"},
	} {
		d, err := g.Q.Delays(lo, hi, row.class)
		if err != nil {
			return Block{}, err
		}
		if d.Count == 0 {
			tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: g.tr(row.label)}, {Text: "0", Num: true},
				{Text: "—", Num: true}, {Text: "—", Num: true}, {Text: "—", Num: true}, {Text: "—", Num: true}, {Text: "—", Num: true}, {Text: "—", Num: true}}})
			continue
		}
		tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: g.tr(row.label)}, {Text: g.fmtInt(float64(d.Count)), Num: true},
			{Text: g.fmtSeconds(d.Avg), Num: true}, {Text: g.fmtSeconds(d.P50), Num: true}, {Text: g.fmtSeconds(d.P90), Num: true},
			{Text: g.fmtSeconds(d.P95), Num: true}, {Text: g.fmtSeconds(d.P99), Num: true}, {Text: g.fmtSeconds(d.Max), Num: true}}})
	}
	return Block{Kind: "card", Title: g.tr("Tempi di consegna"),
		Note:     g.tr("Campo delay= di Postfix, per destinatario consegnato; percentili esatti (nearest-rank) sulle consegne del periodo."),
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
	tlsPct := g.tr("n/d")
	if sess > 0 {
		tlsPct = g.fmtPct(t.Get(ag.MSessionsTLS) / sess * 100)
	}
	rows := []Tr{
		g.row2("Connessioni SMTP in ingresso", g.fmtInt(t.Get(ag.MConnections)), "connessioni"),
		g.row2("Sessioni concluse", g.fmtInt(sess), "sessioni"),
		g.row2("Sessioni con STARTTLS", g.fmtInt(t.Get(ag.MSessionsTLS))+" ("+tlsPct+")", "sessioni"),
		g.row2("Messaggi con autenticazione SASL", g.fmtInt(t.Get(ag.MSASLOK)), "messaggi accettati"),
		g.row2("Autenticazioni SASL fallite", g.fmtInt(t.Get(ag.MSASLFail)), "eventi"),
		g.row2("Errori TLS", g.fmtInt(t.Get(ag.MTLSError)), "eventi"),
		g.row2("Sessioni interrotte (lost connection / timeout)", g.fmtInt(t.Get(ag.MSessionAbort)), "eventi"),
		g.row2("Handshake TLS registrati in ingresso / uscita", g.fmtInt(t.Get(ag.MTLSIn))+" / "+g.fmtInt(t.Get(ag.MTLSOut)), "connessioni"),
	}
	note := g.tr("STARTTLS dalle statistiche di fine sessione di smtpd.")
	if t.Get(ag.MTLSIn)+t.Get(ag.MTLSOut) == 0 {
		note += " " + g.tr("Nessun handshake TLS registrato (tls_loglevel = 0): TLS in uscita non misurabile.")
	}
	return Block{Kind: "card", Title: g.tr("TLS e SASL"), Note: note, Children: []Block{{Kind: "table", Table: &Table{
		ID: "security", Head: []Th{{Text: g.tr("Indicatore")}, {Text: g.tr("Valore"), Num: true}, {Text: g.tr("Unità")}}, Rows: rows}}}}
}

// row2 is a row of the security table: label and unit are source texts.
func (g *Generator) row2(label, value, unit string) Tr {
	return Tr{Cells: []Td{{Text: g.tr(label)}, {Text: value, Num: true}, {Text: g.tr(unit), Title: g.tr(unit)}}}
}

func (g *Generator) compositionCard(days []string) (Block, error) {
	if len(days) == 0 {
		return Block{}, nil
	}
	tbl := &Table{ID: "composition", Sortable: true, Head: []Th{{Text: g.tr("Categoria")}, {Text: g.tr("Voce")}, {Text: g.tr("Conteggio"), Num: true}}}
	for _, d := range []struct{ dim, label string }{{ag.DOrigin, "Istanze di coda per origine"}, {ag.DAgent, "Righe di stato per agente"}} {
		items, _, _, err := g.Q.Ranking(days[0], days[len(days)-1], d.dim, 100)
		if err != nil {
			return Block{}, err
		}
		for _, it := range items {
			tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: g.tr(d.label)}, {Text: it.Key, Key: true},
				{Text: g.fmtInt(float64(it.Value)), Num: true, Sort: fmt.Sprint(it.Value)}}})
		}
	}
	return Block{Kind: "card", Title: g.tr("Composizione del traffico"),
		Note:     g.tr("Le istanze 'reinject' sono il secondo passaggio dei messaggi dopo il content filter e non sono conteggiate come nuovi messaggi."),
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
	tbl := &Table{ID: "comparison", Head: []Th{{Text: g.tr("Metrica")}, {Text: g.tr("Unità")}, {Text: g.tr("Periodo"), Num: true},
		{Text: prevLabel, Num: true}, {Text: g.tr("Variazione"), Num: true}}}
	for _, m := range comparedMetrics {
		c, p := cur.totals.Get(m.metric), prev.totals.Get(m.metric)
		pv := g.fmtUnit(p, m.unit)
		d, _ := g.fmtDelta(c, p)
		if prev.cov.Status == query.Missing {
			pv, d = g.tr("n/d"), g.tr("n/d")
		} else if !comparable {
			d += " *"
		}
		unit := m.unit
		if unit == "bytes" {
			unit = "byte"
		}
		tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: g.tr(m.label)}, {Text: g.tr(unit)}, {Text: g.fmtUnit(c, m.unit), Num: true},
			{Text: pv, Num: true}, {Text: d, Num: true}}})
	}
	note := g.tr("Variazioni calcolate sui periodi interamente osservati.")
	if !comparable {
		note = g.trf("* Copertura diversa: periodo %s, precedente %s. Le variazioni non sono confrontabili.",
			g.fmtPct(cur.cov.Fraction*100), g.fmtPct(prev.cov.Fraction*100))
	}
	return Block{Kind: "card", Title: g.trf("Confronto con %s", prevLabel), Note: note, Children: []Block{{Kind: "table", Table: tbl}}}
}

// ---- pages ----

func section(title, note string, children ...Block) Block {
	return Block{Kind: "section", Title: title, Note: note, Children: children}
}

// tabs groups sections into a tab set: the browser shows one section at a
// time, all of them without JavaScript and when printing. A section title is
// its tab label and, as a slug, the panel id (#id selects the tab). Empty
// sections are dropped.
func tabs(sections ...Block) Block {
	var c []Block
	for _, s := range sections {
		if s.Kind == "" || isEmpty(s) {
			continue
		}
		s.ID = slug(s.Title)
		c = append(c, s)
	}
	return Block{Kind: "tabs", Children: c}
}

// isEmpty reports whether a container block has no content at all.
func isEmpty(b Block) bool {
	switch b.Kind {
	case "section", "grid", "card", "tabs":
		for _, c := range b.Children {
			if !isEmpty(c) {
				return false
			}
		}
		return true
	}
	return b.Kind == ""
}

// slug turns a title into an ASCII identifier ("Rifiuti ed
// errori" → "rifiuti-ed-errori").
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch r {
		case 'à', 'á':
			r = 'a'
		case 'è', 'é':
			r = 'e'
		case 'ì', 'í':
			r = 'i'
		case 'ò', 'ó':
			r = 'o'
		case 'ù', 'ú':
			r = 'u'
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return b.String()
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
	pg := g.basePage(root, g.title(p), string(p.Kind), p)
	pg.Crumbs = g.crumbs(root, p)
	pg.Prev = g.link(root, p.Prev(), g.shortTitle(p.Prev()))
	pg.Next = g.link(root, p.Next(), g.shortTitle(p.Next()))
	if p.Kind != period.Year {
		up := period.Of(map[period.Kind]period.Kind{period.Day: period.Month, period.Week: period.Month, period.Month: period.Year}[p.Kind], p.Start, g.loc())
		pg.Up = g.link(root, up, g.shortTitle(up))
	}
	pg.Subtitle = g.trf("Copertura: %s · %s", g.statusText(pd.cov), g.observedSummary(pd))
	pg.Banners = g.coverageBanners(pd)
	if pd.cov.Status == query.Missing {
		pg.Blocks = append(pg.Blocks, section(g.tr("Copertura"), g.tr("Nessun dato da mostrare."), g.coverageTable(pd)))
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
	return fmt.Sprintf("%d/%d %s", n, len(pd.children), g.tr(unit))
}

func (g *Generator) crumbs(root string, p period.Period) []Link {
	c := []Link{{Href: root + "index.html", Text: g.tr("Panoramica")}}
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
	tbl := &Table{ID: "coverage", Head: []Th{{Text: g.tr("Intervallo")}, {Text: g.tr("Copertura")}, {Text: g.tr("Osservato"), Num: true}}}
	for _, b := range pd.children {
		tbl.Rows = append(tbl.Rows, Tr{Class: b.Status, Cells: []Td{{Text: g.bucketLabel(pd.p.Kind, b)},
			{Text: g.statusText(b), Badge: badgeClass(b.Status)}, {Text: g.fmtSeconds(float64(b.Covered)), Num: true}}})
	}
	return card(g.tr("Copertura per intervallo"), g.tr("Un intervallo senza righe di log non è traffico zero: è un dato assente."), Block{Kind: "table", Table: tbl})
}

func (g *Generator) bucketLabel(k period.Kind, b query.Bucket) string {
	switch k {
	case period.Day:
		return b.Key + ":00–" + b.Key + ":59"
	case period.Year:
		t := time.Unix(b.Start, 0).In(g.loc())
		return capitalize(g.monthName(t.Month()))
	default:
		t := time.Unix(b.Start, 0).In(g.loc())
		return g.weekday(t) + " " + t.Format(g.l().dayMonthFmt)
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
	delays, err := g.delayTable("delays", pd.p.Start.Unix(), pd.p.End.Unix())
	if err != nil {
		return err
	}
	vol := g.bucketChart("volume", "Volume orario", "bar", "bytes", pd.children, labels, hourShort(pd.children), nil, vals,
		[]seriesDef{{"Volume accettato", ag.MBytes, colBytes}})
	comp, tail, err := g.tailSections(pd)
	if err != nil {
		return err
	}
	pg.Blocks = append(pg.Blocks,
		section(g.tr("Indicatori del giorno"), g.tr("Variazione rispetto al giorno precedente, solo se entrambi i giorni sono interamente osservati."),
			g.kpis(pd.totals, prev.totals, pd.cov.Status == query.Complete && prev.cov.Status == query.Complete)),
		tabs(append([]Block{
			section(g.tr("Traffico"), g.tr("Ore locali 00–23. Le ore senza dati sono tratteggiate e non valgono zero."),
				card(g.tr("Traffico orario"), "", g.bucketChart("hourly", "Traffico orario", "bar", "", pd.children, labels, hourShort(pd.children), nil, vals, trafficSeries)),
				grid(card(g.tr("Volume orario (byte accettati)"), "", vol), comp)),
			section(g.tr("Tempi di consegna"), "", delays),
		}, tail...)...),
	)
	return nil
}

func hourShort(b []query.Bucket) []string {
	s := make([]string, len(b))
	for i, x := range b {
		s[i] = x.Key
	}
	return s
}

// Rankings grouped by tab.
var (
	rankTraffic  = pickRankings(ag.DSenderDomain, ag.DRcptDomain, ag.DClientIP, ag.DRelay)
	rankErrors   = pickRankings(ag.DRejectReason, ag.DRejectIP, ag.DDeferReason, ag.DBounceReason)
	rankSecurity = pickRankings(ag.DSASLUser, ag.DSASLFailIP)
)

func pickRankings(dims ...string) []rankDef {
	var out []rankDef
	for _, d := range dims {
		for _, r := range rankings {
			if r.dim == d {
				out = append(out, r)
			}
		}
	}
	return out
}

// tailSections returns the tabs shared by every period page (rankings,
// security, coverage) and, separately, the traffic composition card, which
// belongs to the page's own traffic tab.
func (g *Generator) tailSections(pd *periodData) (Block, []Block, error) {
	kind := string(pd.p.Kind)
	note := g.trf("Primi %d valori per categoria; tabelle ordinabili e filtrabili.", g.Cfg.Reports.TopLimit)
	traffic, err := g.rankingCards(kind, pd.days, rankTraffic)
	if err != nil {
		return Block{}, nil, err
	}
	errs, err := g.rankingCards(kind, pd.days, rankErrors)
	if err != nil {
		return Block{}, nil, err
	}
	sec, err := g.rankingCards(kind, pd.days, rankSecurity)
	if err != nil {
		return Block{}, nil, err
	}
	comp, err := g.compositionCard(pd.days)
	if err != nil {
		return Block{}, nil, err
	}
	return comp, []Block{
		section(g.tr("Mittenti e destinatari"), note, grid(traffic...)),
		section(g.tr("Rifiuti ed errori"), note, grid(errs...)),
		section(g.tr("Sicurezza"), "", grid(append([]Block{g.securityCard(pd.totals)}, sec...)...)),
		section(g.tr("Copertura"), "", g.coverageTable(pd)),
	}, nil
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
		l[i] = g.weekday(t) + " " + t.Format(g.l().dateFmt)
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
		short[i] = g.weekdayShort(time.Unix(b.Start, 0).In(g.loc())) + " " + short[i]
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
	comp, tail, err := g.tailSections(pd)
	if err != nil {
		return err
	}
	pg.Blocks = append(pg.Blocks,
		section(g.tr("Indicatori della settimana"), g.tr("Variazione rispetto alla settimana precedente, solo se entrambe sono interamente osservate."), g.kpis(pd.totals, prev.totals, comparable)),
		tabs(append([]Block{
			section(g.tr("Traffico"), g.tr("Da lunedì a domenica (settimana ISO 8601)."),
				card(g.tr("Traffico giornaliero"), g.tr("Clic su un giorno per aprire il report giornaliero."),
					g.bucketChart("daily", "Traffico giornaliero", "bar", "", pd.children, labels, short, links, vals, trafficSeries)),
				grid(card(g.tr("Distribuzione oraria aggregata"), g.tr("Somma, per ora del giorno, dei giorni osservati della settimana."),
					g.bucketChart("hourdist", "Distribuzione oraria", "bar", "", hb, hl, hourShort(hb), nil, byHour[:], trafficSeries)), comp)),
			section(g.tr("Confronto"), "", g.comparison(pd, prev, g.trf("settimana %s", prev.p.Key))),
			section(g.tr("Tempi di consegna"), "", delays, card(g.tr("Andamento dei ritardi"), "", trend)),
		}, tail...)...),
	)
	return nil
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
	comp, tail, err := g.tailSections(pd)
	if err != nil {
		return err
	}
	pg.Blocks = append(pg.Blocks,
		section(g.tr("Indicatori del mese"), g.tr("Variazione rispetto al mese precedente, solo se entrambi sono interamente osservati."), g.kpis(pd.totals, prev.totals, comparable)),
		tabs(append([]Block{
			section(g.tr("Traffico"), "",
				card(g.tr("Traffico per giorno"), g.tr("Clic su un giorno per aprire il report giornaliero."),
					g.bucketChart("daily", "Traffico per giorno", "bar", "", pd.children, labels, short, links, vals, trafficSeries)),
				grid(g.topDays(pd, vals, labels, links), g.weekdayTable(pd, vals)), comp),
			section(g.tr("Confronto"), "", g.comparison(pd, prev, g.monthName(prev.p.Start.Month())+" "+prev.p.Start.Format("2006"))),
			section(g.tr("Tempi di consegna"), "", delays, card(g.tr("Andamento dei ritardi"), "", trend)),
		}, tail...)...),
	)
	return nil
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
	tbl := &Table{ID: "topdays", Sortable: true, Head: []Th{{Text: g.tr("Giorno")}, {Text: g.tr("Copertura")}, {Text: g.tr("Messaggi accettati"), Num: true},
		{Text: g.tr("Consegne riuscite"), Num: true}, {Text: g.tr("Rifiuti"), Num: true}}}
	for _, d := range ds {
		b := pd.children[d.i]
		tbl.Rows = append(tbl.Rows, Tr{Class: b.Status, Cells: []Td{{Text: labels[d.i], Href: links[d.i]},
			{Text: g.statusText(b), Badge: badgeClass(b.Status)},
			{Text: g.fmtInt(d.v), Num: true, Sort: fmt.Sprint(d.v)},
			{Text: g.fmtInt(vals[d.i].Get(ag.MSent)), Num: true, Sort: fmt.Sprint(vals[d.i].Get(ag.MSent))},
			{Text: g.fmtInt(vals[d.i].Get(ag.MRejects)), Num: true, Sort: fmt.Sprint(vals[d.i].Get(ag.MRejects))}}})
	}
	return card(g.tr("Giorni con maggiore traffico"), g.tr("Ordinati per messaggi accettati; solo giorni con dati."), Block{Kind: "table", Table: tbl})
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
	tbl := &Table{ID: "weekdays", Sortable: true, Head: []Th{{Text: g.tr("Giorno della settimana")}, {Text: g.tr("Giorni con dati"), Num: true},
		{Text: g.tr("Messaggi accettati"), Num: true}, {Text: g.tr("Media per giorno"), Num: true}, {Text: g.tr("Consegne riuscite"), Num: true}, {Text: g.tr("Rifiuti"), Num: true}}}
	for w := 0; w < 7; w++ {
		avg := g.tr("n/d")
		if n[w] > 0 {
			avg = g.fmtDec(sum[w].Get(ag.MAccepted)/float64(n[w]), 1)
			if partial[w] {
				avg += " *"
			}
		}
		tbl.Rows = append(tbl.Rows, Tr{Cells: []Td{{Text: g.l().weekdays[w], Sort: fmt.Sprint(w)}, {Text: fmt.Sprint(n[w]), Num: true, Sort: fmt.Sprint(n[w])},
			{Text: g.fmtInt(sum[w].Get(ag.MAccepted)), Num: true, Sort: fmt.Sprint(sum[w].Get(ag.MAccepted))},
			{Text: avg, Num: true}, {Text: g.fmtInt(sum[w].Get(ag.MSent)), Num: true, Sort: fmt.Sprint(sum[w].Get(ag.MSent))},
			{Text: g.fmtInt(sum[w].Get(ag.MRejects)), Num: true, Sort: fmt.Sprint(sum[w].Get(ag.MRejects))}}})
	}
	return card(g.tr("Distribuzione per giorno della settimana"), g.tr("Media sui soli giorni con dati; * = include giorni a copertura parziale."), Block{Kind: "table", Table: tbl})
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
		labels[i] = g.monthName(m.Start.Month()) + " " + m.Start.Format("2006")
		short[i] = g.monthShort(m.Start.Month())
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
	summary := &Table{ID: "months", Sortable: true, Head: []Th{{Text: g.tr("Mese")}, {Text: g.tr("Copertura")}, {Text: g.tr("Messaggi"), Num: true},
		{Text: g.tr("Destinatari"), Num: true}, {Text: g.tr("Volume"), Num: true}, {Text: g.tr("Consegne"), Num: true}, {Text: g.tr("Differiti"), Num: true},
		{Text: g.tr("Fallimenti"), Num: true}, {Text: g.tr("Rifiuti"), Num: true}}}
	for i, b := range pd.children {
		r := Tr{Class: b.Status, Cells: []Td{{Text: labels[i], Href: links[i], Sort: fmt.Sprint(i)}, {Text: g.statusText(b), Badge: badgeClass(b.Status)}}}
		for _, m := range []struct{ k, u string }{{ag.MAccepted, ""}, {ag.MRecipients, ""}, {ag.MBytes, "bytes"}, {ag.MSent, ""},
			{ag.MDeferred, ""}, {ag.MFinalFailures, ""}, {ag.MRejects, ""}} {
			if b.Status == query.Missing {
				r.Cells = append(r.Cells, Td{Text: g.tr("n/d"), Num: true, Sort: ""})
				continue
			}
			v := vals[i].Get(m.k)
			r.Cells = append(r.Cells, Td{Text: g.fmtUnit(v, m.u), Num: true, Sort: fmt.Sprint(v)})
		}
		summary.Rows = append(summary.Rows, r)
	}
	comp, tail, err := g.tailSections(pd)
	if err != nil {
		return err
	}
	pg.Blocks = append(pg.Blocks,
		section(g.tr("Indicatori dell'anno"), g.tr("Variazione rispetto all'anno precedente, solo se entrambi sono interamente osservati."), g.kpis(pd.totals, prev.totals, comparable)),
		tabs(append([]Block{
			section(g.tr("Traffico"), "",
				card(g.tr("Messaggi per mese"), g.tr("Clic su un mese per aprire il report mensile."),
					g.bucketChart("monthly", "Traffico mensile", "bar", "", pd.children, labels, short, links, vals, trafficSeries)),
				grid(card(g.tr("Andamento di rifiuti, differimenti e fallimenti"), "",
					g.bucketChart("trend", "Anomalie mensili", "line", "", pd.children, labels, short, links, vals, trafficSeries[2:])),
					card(g.tr("Volume mensile"), "", g.bucketChart("volume", "Volume mensile", "bar", "bytes", pd.children, labels, short, links, vals,
						[]seriesDef{{"Volume accettato", ag.MBytes, colBytes}}))), comp),
			section(g.tr("Riepilogo mensile"), "", card(g.tr("Riepilogo dei dodici mesi"), "", Block{Kind: "table", Table: summary})),
			section(g.tr("Confronto"), "", g.comparison(pd, prev, g.trf("anno %s", prev.p.Key))),
			section(g.tr("Tempi di consegna"), "", delays, card(g.tr("Tempi medi e percentili per mese"), "", trend)),
		}, tail...)...),
	)
	return nil
}

// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package report renders the static HTML dashboards from the query layer.
package report

import (
	"encoding/json"
	"html/template"
	"math"
	"strconv"
	"strings"
)

// Page is the model of one HTML page.
type Page struct {
	Root      string // relative prefix to the output root ("" or "../")
	SiteTitle string
	Title     string
	Subtitle  string
	Crumbs    []Link
	Prev      *Link
	Up        *Link
	Next      *Link
	Nav       []NavItem
	Banners   []Banner
	Blocks    []Block
	Generated string
	DataAsOf  string
	Version   string
	Privacy   string
}

// Link is a hyperlink.
type Link struct {
	Href string
	Text string
}

// NavItem is a top-bar entry.
type NavItem struct {
	Href   string
	Text   string
	Active bool
}

// Banner is a highlighted notice (info, warn, bad, ok).
type Banner struct {
	Level string
	Text  string
}

// Block is a page component. Exactly one of the content fields is used,
// according to Kind: "section" (Children), "grid" (Children), "card"
// (Children), "kpis", "chart", "table", "archive", "text".
type Block struct {
	Kind     string
	Title    string
	Note     string
	Text     string
	KPIs     []KPI
	Chart    *Chart
	Table    *Table
	Archive  []ArchiveYear
	Children []Block
}

// KPI is a metric tile.
type KPI struct {
	Label      string
	Value      string
	Unit       string
	Class      string
	Delta      string
	DeltaClass string
	Title      string
}

// Chart is an SVG chart rendered client-side from embedded JSON. Table holds
// the same data for printing and for browsers without JavaScript.
type Chart struct {
	ID    string
	JSON  template.JS
	Table *Table
}

// Table is a data table.
type Table struct {
	ID       string
	Head     []Th
	Rows     []Tr
	Sortable bool
	Filter   bool
	Empty    string
}

// Th is a header cell.
type Th struct {
	Text string
	Num  bool
}

// Tr is a table row.
type Tr struct {
	Class string
	Cells []Td
}

// Td is a table cell.
type Td struct {
	Text  string
	Href  string
	Num   bool
	Key   bool
	Sort  string  // numeric sort value (data-v)
	Bar   float64 // 0..1: inline proportional bar
	Badge string  // coverage badge class
	Title string
}

// ArchiveYear is one year of the archive calendar on the index.
type ArchiveYear struct {
	Year   string
	Href   string
	Months []ArchiveMonth
	Total  string
}

// ArchiveMonth is one cell of the archive calendar.
type ArchiveMonth struct {
	Label  string
	Href   string
	Status string
	Value  string
}

// chartSpec is the JSON consumed by report.js.
type chartSpec struct {
	Type       string       `json:"type"`
	Title      string       `json:"title"`
	Labels     []string     `json:"labels"`
	Short      []string     `json:"short,omitempty"`
	Links      []*string    `json:"links,omitempty"`
	Status     []string     `json:"status,omitempty"`
	Fraction   []float64    `json:"fraction,omitempty"`
	Unit       string       `json:"unit,omitempty"`
	Stacked    bool         `json:"stacked,omitempty"`
	Height     int          `json:"height,omitempty"`
	HasMissing bool         `json:"hasMissing,omitempty"`
	Series     []seriesSpec `json:"series"`
}

type seriesSpec struct {
	Name   string     `json:"name"`
	Color  string     `json:"color"`
	Values []*float64 `json:"values"`
}

// toJS marshals v for embedding in <script type="application/json">.
// encoding/json escapes <, > and & so the content cannot close the element.
func toJS(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only plain data types are marshalled
	}
	return template.JS(b)
}

// Series colors (kept in sync with report.css).
const (
	colAccepted = "#2f6fd6"
	colSent     = "#1e9a55"
	colDeferred = "#d98a0b"
	colFailed   = "#d33b3b"
	colRejected = "#7a4cc2"
	colOther    = "#6b7a8c"
	colBytes    = "#3a8fb7"
)

// ---- Italian number formatting ----

func groupThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// fmtInt formats an integer with "." thousands separators.
func fmtInt(v float64) string {
	return groupThousands(strconv.FormatInt(int64(math.Round(v)), 10))
}

// fmtDec formats with d decimals and "," as decimal separator.
func fmtDec(v float64, d int) string {
	s := strconv.FormatFloat(v, 'f', d, 64)
	ip, fp, ok := strings.Cut(s, ".")
	if !ok {
		return groupThousands(ip)
	}
	return groupThousands(ip) + "," + fp
}

// fmtBytes formats a byte count with decimal units.
func fmtBytes(v float64) string {
	units := []string{"B", "kB", "MB", "GB", "TB", "PB"}
	i := 0
	for math.Abs(v) >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if i == 0 {
		return fmtInt(v) + " B"
	}
	return fmtDec(v, 1) + " " + units[i]
}

// fmtSeconds formats a duration in seconds.
func fmtSeconds(v float64) string {
	switch {
	case v < 60:
		return fmtDec(v, 2) + " s"
	case v < 3600:
		return fmtDec(v/60, 1) + " min"
	case v < 86400:
		return fmtDec(v/3600, 1) + " h"
	default:
		return fmtDec(v/86400, 1) + " g"
	}
}

// fmtPct formats a percentage.
func fmtPct(v float64) string { return fmtDec(v, 1) + "%" }

// fmtDelta formats a relative change and returns its CSS class.
func fmtDelta(cur, prev float64) (string, string) {
	if prev == 0 {
		if cur == 0 {
			return "0,0%", "na"
		}
		return "n/d", "na"
	}
	d := (cur - prev) / prev * 100
	s := fmtDec(d, 1) + "%"
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

func f64(v float64) *float64 { return &v }

func strp(s string) *string { return &s }

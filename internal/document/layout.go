// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Adapted from the local adscan document engine at the owner's request.
package document

import (
	"context"
	"embed"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/signintech/gopdf"
)

//go:embed fonts/*
var documentFonts embed.FS

const (
	pageWidth  = 595.28
	pageHeight = 841.89
	pageLeft   = 42.0
	bodyWidth  = pageWidth - 2*pageLeft
	bodyTop    = 92.0
	// Leave a reserve below the content, including in editable ODT documents.
	bodyBottom = 754.0
)

type documentCell struct {
	Lines      []string
	Width      float64
	AlignRight bool
}
type documentItem struct {
	Image                         []byte
	Cells                         []documentCell
	Size, LineHeight, Height, Gap float64
	Level                         int
	Bold, Table, TableHeader      bool
	URL, Anchor                   string
	Indent                        float64
	ContentsIndex                 int // One-based index into documentLayout.Contents; zero for body/title.
}
type documentPage struct {
	Items    []documentItem
	Contents bool
}
type contentsEntry struct {
	Title, Anchor string
	Page          int
	Level         int
}
type documentLayout struct {
	Document            Document
	Pages               []documentPage
	Contents            []contentsEntry
	TOCTitle, PageLabel string
	ContentsPages       int
	Header              headerRow
}

// headerRow is shared by PDF and ODT: server, period, right-aligned UTC date.
type headerRow struct {
	Values [3]string
	X      [3]float64
	Size   float64
}

func (m *documentMetrics) header(d Document) (headerRow, error) {
	title := d.HeaderTitle
	if title == "" {
		title = d.Title
	}
	h := headerRow{Values: [3]string{d.Subtitle, title, d.Date.UTC().Format("2006-01-02 15:04:05 UTC")}}
	for i := range h.Values {
		h.Values[i] = strings.Join(strings.Fields(h.Values[i]), " ")
	}
	for size := 8.0; size >= 4; size -= 0.25 {
		var widths [3]float64
		for i, value := range h.Values {
			if err := m.font(size, i == 0); err != nil {
				return h, err
			}
			width, err := m.pdf.MeasureTextWidth(value)
			if err != nil {
				return h, err
			}
			widths[i] = width
		}
		h.X = [3]float64{pageLeft, (pageWidth - widths[1]) / 2, pageWidth - pageLeft - widths[2]}
		if h.X[0]+widths[0]+10 <= h.X[1] && h.X[1]+widths[1]+10 <= h.X[2] {
			h.Size = size
			return h, nil
		}
	}
	return h, fmt.Errorf("document header exceeds single-line capacity")
}

type documentMetrics struct {
	ctx     context.Context
	pdf     gopdf.GoPdf
	missing rune
}

func newDocumentMetrics(ctx context.Context) (*documentMetrics, error) {
	m := &documentMetrics{ctx: ctx}
	m.pdf.Start(gopdf.Config{PageSize: gopdf.Rect{W: pageWidth, H: pageHeight}})
	for _, style := range []string{"Regular", "Bold"} {
		data, err := documentFonts.ReadFile("fonts/LiberationSans-" + style + ".ttf")
		if err != nil {
			return nil, err
		}
		option := gopdf.TtfOption{OnGlyphNotFound: func(r rune) {
			if m.missing == 0 {
				m.missing = r
			}
		}}
		if err := m.pdf.AddTTFFontDataWithOption(style, data, option); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *documentMetrics) font(size float64, bold bool) error {
	if err := m.ctx.Err(); err != nil {
		return err
	}
	name := "Regular"
	if bold {
		name = "Bold"
	}
	return m.pdf.SetFont(name, "", size)
}

// wrap breaks long identifiers at rune boundaries, preserving their characters.
// Whitespace is normalized for display; characters are retained in the document.
func (m *documentMetrics) wrap(text string, width, size float64, bold bool) ([]string, error) {
	if err := m.font(size, bold); err != nil {
		return nil, err
	}
	var lines []string
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		words := strings.FieldsFunc(paragraph, func(r rune) bool { return unicode.IsSpace(r) })
		line := ""
		for _, word := range words {
			if err := m.ctx.Err(); err != nil {
				return nil, err
			}
			candidate := word
			if line != "" {
				candidate = line + " " + word
			}
			w, err := m.pdf.MeasureTextWidth(candidate)
			if err != nil {
				return nil, err
			}
			if w <= width {
				line = candidate
				continue
			}
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			for i, r := range word {
				if i%128 == 0 {
					if err := m.ctx.Err(); err != nil {
						return nil, err
					}
				}
				candidate = line + string(r)
				w, err = m.pdf.MeasureTextWidth(candidate)
				if err != nil {
					return nil, err
				}
				if w > width && line != "" {
					lines = append(lines, line)
					line = string(r)
				} else {
					line = candidate
				}
			}
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func paginateDocument(ctx context.Context, d Document, tocTitle, pageLabel string) (documentLayout, error) {
	l := documentLayout{Document: d, Pages: make([]documentPage, 1), TOCTitle: tocTitle, PageLabel: pageLabel}
	m, err := newDocumentMetrics(ctx)
	if err != nil {
		return l, err
	}
	l.Header, err = m.header(d)
	if err != nil {
		return l, err
	}
	y := bodyTop
	newPage := func() { l.Pages = append(l.Pages, documentPage{}); y = bodyTop }
	add := func(item documentItem) {
		l.Pages[len(l.Pages)-1].Items = append(l.Pages[len(l.Pages)-1].Items, item)
		y += item.Height + item.Gap
	}
	for bi, b := range d.Blocks {
		if len(l.Pages) > 2000 {
			return l, fmt.Errorf("document exceeds 2000 pages")
		}
		if err := ctx.Err(); err != nil {
			return l, err
		}
		if b.BreakBefore && y > bodyTop {
			newPage()
		}
		if len(b.Image) > 0 {
			const height = 225.0
			if y+height+12 > bodyBottom {
				newPage()
			}
			add(documentItem{Image: b.Image, Height: height, Gap: 12, Size: 10, LineHeight: 14})
			continue
		}
		if len(b.Headers) > 0 {
			// Equal columns avoid platform-dependent auto-sizing. Long cell values
			// wrap explicitly; every fragment receives a unique ODF table name.
			width := bodyWidth / float64(len(b.Headers))
			rowItem := func(cells []string, header bool) (documentItem, error) {
				item := documentItem{Size: 8, LineHeight: 11, Table: true, TableHeader: header, Bold: header}
				maxLines := 1
				for _, v := range cells {
					lines, err := m.wrap(v, width-8, 8, header)
					if err != nil {
						return item, err
					}
					item.Cells = append(item.Cells, documentCell{Lines: lines, Width: width})
					maxLines = max(maxLines, len(lines))
				}
				item.Height = float64(maxLines) * item.LineHeight
				return item, nil
			}
			header, err := rowItem(b.Headers, true)
			if err != nil {
				return l, err
			}
			if y+header.Height+22 > bodyBottom {
				newPage()
			}
			add(header)
			for _, row := range b.Rows {
				if err := ctx.Err(); err != nil {
					return l, err
				}
				item, err := rowItem(row, false)
				if err != nil {
					return l, err
				}
				remaining := int(item.Height / item.LineHeight)
				offset := 0
				for remaining > 0 {
					if len(l.Pages) > 2000 {
						return l, fmt.Errorf("document exceeds 2000 pages")
					}
					if err := ctx.Err(); err != nil {
						return l, err
					}
					if y+item.LineHeight > bodyBottom || (item.Height <= bodyBottom-bodyTop-header.Height && item.Height > bodyBottom-y && offset == 0) {
						newPage()
						add(header)
					}
					n := min(remaining, int(math.Floor((bodyBottom-y)/item.LineHeight)))
					if n < 1 {
						return l, fmt.Errorf("table header exceeds page capacity")
					}
					part := item
					part.Height = float64(n) * item.LineHeight
					part.Cells = nil
					for _, c := range item.Cells {
						start := min(offset, len(c.Lines))
						end := min(offset+n, len(c.Lines))
						part.Cells = append(part.Cells, documentCell{Lines: c.Lines[start:end], Width: c.Width})
					}
					add(part)
					offset += n
					remaining -= n
				}
			}
			continue
		}
		size, lineHeight, gap := 10.0, 14.0, 5.0
		if b.Level == 1 {
			size, lineHeight, gap = 18, 24, 12
		} else if b.Level == 2 {
			size, lineHeight, gap = 12, 17, 8
		} else if b.Level == 3 {
			size, lineHeight, gap = 11, 16, 6
		}
		lines, err := m.wrap(b.Text, bodyWidth-2, size, b.Level > 0)
		if err != nil {
			return l, err
		}
		if b.Level == 1 && len(l.Pages[len(l.Pages)-1].Items) > 0 {
			newPage()
		}
		// Keep headings with the following chart and its brief explanation,
		// or at least two body lines for ordinary sections.
		reserve := 28.0
		if b.Level > 0 {
			for _, next := range d.Blocks[bi+1 : min(len(d.Blocks), bi+4)] {
				if next.Level > 0 || len(next.Headers) > 0 {
					break
				}
				if len(next.Image) > 0 {
					reserve += 225 + 12
					break
				}
				wrapped, err := m.wrap(next.Text, bodyWidth-2, 10, false)
				if err != nil {
					return l, err
				}
				reserve += float64(len(wrapped))*14 + 5
			}
			reserve = min(reserve, bodyBottom-bodyTop-float64(len(lines))*lineHeight-gap)
		}
		if b.Level > 0 && y+float64(len(lines))*lineHeight+gap+reserve > bodyBottom {
			newPage()
		}
		anchor := ""
		if b.Level > 0 && b.Level <= d.contentsDepth() {
			anchor = fmt.Sprintf("section-%d", len(l.Contents)+1)
			l.Contents = append(l.Contents, contentsEntry{Title: b.Text, Anchor: anchor, Page: len(l.Pages), Level: b.Level})
		}
		for len(lines) > 0 {
			if len(l.Pages) > 2000 {
				return l, fmt.Errorf("document exceeds 2000 pages")
			}
			if err := ctx.Err(); err != nil {
				return l, err
			}
			if y+lineHeight > bodyBottom {
				newPage()
			}
			n := min(len(lines), int(math.Floor((bodyBottom-y)/lineHeight)))
			item := documentItem{Cells: []documentCell{{Lines: lines[:n], Width: bodyWidth}}, Size: size, LineHeight: lineHeight, Height: float64(n) * lineHeight, Gap: gap, Level: b.Level, Bold: b.Level > 0, URL: b.URL, Anchor: anchor}
			add(item)
			anchor = ""
			lines = lines[n:]
		}
	}
	// Body destinations start relative to the first content page. The index
	// has a fixed number column, so its pagination is independent of the page
	// numbers printed there. Insert cover + index once, then offset destinations.
	toc, err := paginateContents(ctx, m, l.Contents, tocTitle)
	if err != nil {
		return l, err
	}
	l.ContentsPages = len(toc)
	offset := 1 + len(toc)
	for i := range l.Contents {
		l.Contents[i].Page += offset
	}
	for pi := range toc {
		for ii := range toc[pi].Items {
			item := &toc[pi].Items[ii]
			if item.ContentsIndex == 0 {
				continue
			}
			e := l.Contents[item.ContentsIndex-1]
			if err := m.font(item.Size, item.Bold); err != nil {
				return l, err
			}
			width, err := m.pdf.MeasureTextWidth(fmt.Sprint(e.Page))
			if err != nil {
				return l, err
			}
			if width > 48 {
				return l, fmt.Errorf("contents page number exceeds reserved column")
			}
			// Page numbers align with the final title line, like the ODF tab stop.
			cell := &item.Cells[1]
			cell.Lines = make([]string, len(item.Cells[0].Lines))
			cell.Lines[len(cell.Lines)-1] = fmt.Sprint(e.Page)
		}
	}
	pages := make([]documentPage, 0, 1+len(toc)+len(l.Pages))
	pages = append(pages, documentPage{})
	pages = append(pages, toc...)
	l.Pages = append(pages, l.Pages...)
	return l, nil
}

func contentsFormat(level int) documentItem {
	if level == 1 {
		return documentItem{Size: 11, LineHeight: 16, Gap: 14, Bold: true}
	}
	return documentItem{Size: 10, LineHeight: 14, Gap: 4, Indent: float64(level-1) * 14}
}

func paginateContents(ctx context.Context, m *documentMetrics, entries []contentsEntry, title string) ([]documentPage, error) {
	lines, err := m.wrap(title, bodyWidth, 18, true)
	if err != nil {
		return nil, err
	}
	heading := documentItem{Cells: []documentCell{{Lines: lines, Width: bodyWidth}}, Size: 18, LineHeight: 24, Height: float64(len(lines)) * 24, Gap: 18, Bold: true}
	capacity := bodyBottom - bodyTop
	if heading.Height+heading.Gap+18 > capacity {
		return nil, fmt.Errorf("contents title exceeds page capacity")
	}
	pages := []documentPage{{Items: []documentItem{heading}, Contents: true}}
	height := heading.Height + heading.Gap
	for i, e := range entries {
		if len(pages) > 2000 {
			return nil, fmt.Errorf("contents exceeds 2000 pages")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item := contentsFormat(e.Level)
		lines, err := m.wrap(e.Title, bodyWidth-item.Indent-48, item.Size, item.Bold)
		if err != nil {
			return nil, err
		}
		item.Height = float64(len(lines)) * item.LineHeight
		if item.Height+item.Gap+heading.Height+heading.Gap > capacity {
			return nil, fmt.Errorf("contents entry exceeds page capacity")
		}
		if height+item.Height+item.Gap > capacity {
			pages = append(pages, documentPage{Items: []documentItem{heading}, Contents: true})
			height = heading.Height + heading.Gap
		}
		item.Cells = []documentCell{{Lines: lines, Width: bodyWidth - item.Indent - 48}, {Width: 48, AlignRight: true}}
		item.URL = "#" + e.Anchor
		item.ContentsIndex = i + 1
		pages[len(pages)-1].Items = append(pages[len(pages)-1].Items, item)
		height += item.Height + item.Gap
	}
	return pages, nil
}

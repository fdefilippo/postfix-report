// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Adapted from the local adscan document engine at the owner's request.
package document

import (
	"context"
	"fmt"
	"strings"

	"github.com/signintech/gopdf"
)

func writePDF(ctx context.Context, l documentLayout) ([]byte, error) {
	m, err := newDocumentMetrics(ctx)
	if err != nil {
		return nil, err
	}
	p := &m.pdf
	p.SetInfo(gopdf.PdfInfo{Title: l.Document.Title, Subject: l.Document.Subtitle, Creator: l.Document.Creator, CreationDate: l.Document.Date})
	write := func(text string, x, y, size float64, bold bool) error {
		if err := m.font(size, bold); err != nil {
			return err
		}
		// PDF uses the same fill colour for glyphs and filled shapes. Reset it
		// after table backgrounds so text keeps its intended contrast.
		p.SetTextColor(25, 35, 45)
		p.SetXY(x, y+size)
		return p.Text(text)
	}
	for i, page := range l.Pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.AddPage()
		if i == 0 {
			y := 260.0
			for _, part := range []struct {
				text string
				size float64
				bold bool
			}{{l.Document.Title, 24, true}, {l.Document.Subtitle, 16, false}, {l.Document.CoverNote, 12, false}, {l.Document.Date.UTC().Format("2006-01-02 15:04:05 UTC"), 10, false}} {
				if part.text == "" {
					continue
				}
				lines, err := m.wrap(part.text, bodyWidth, part.size, part.bold)
				if err != nil {
					return nil, err
				}
				for _, line := range lines {
					w, err := p.MeasureTextWidth(line)
					if err != nil {
						return nil, err
					}
					if err := write(line, (pageWidth-w)/2, y, part.size, part.bold); err != nil {
						return nil, err
					}
					y += part.size * 1.4
				}
				y += 14
			}
			continue
		}
		for j, value := range l.Header.Values {
			if err := write(value, l.Header.X[j], 45, l.Header.Size, j == 0); err != nil {
				return nil, err
			}
		}
		p.SetStrokeColor(175, 185, 198)
		p.SetLineWidth(0.4)
		p.Line(pageLeft, 74, pageWidth-pageLeft, 74)
		if err := write(fmt.Sprintf("%s %d / %d", l.PageLabel, i+1, len(l.Pages)), pageLeft, 794, 9, false); err != nil {
			return nil, err
		}
		y := bodyTop
		for _, item := range page.Items {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(item.Image) > 0 {
				h, err := gopdf.ImageHolderByBytes(item.Image)
				if err != nil {
					return nil, err
				}
				if err = p.ImageByHolder(h, pageLeft, y, &gopdf.Rect{W: bodyWidth, H: item.Height}); err != nil {
					return nil, err
				}
				y += item.Height + item.Gap
				continue
			}
			if item.Anchor != "" {
				p.SetXY(pageLeft, y)
				p.SetAnchor(item.Anchor)
				p.AddOutline(item.Cells[0].Lines[0])
			}
			x := pageLeft + item.Indent
			for _, cell := range item.Cells {
				padding := 0.0
				if item.Table {
					padding = 4
				}
				if item.TableHeader {
					p.SetFillColor(233, 238, 245)
					p.RectFromUpperLeftWithStyle(x, y, cell.Width, item.Height, "F")
				}
				for n, line := range cell.Lines {
					textX := x + padding
					if cell.AlignRight {
						if err := m.font(item.Size, item.Bold); err != nil {
							return nil, err
						}
						width, err := p.MeasureTextWidth(line)
						if err != nil {
							return nil, err
						}
						textX = x + cell.Width - width
					}
					if err := write(line, textX, y+float64(n)*item.LineHeight, item.Size, item.Bold); err != nil {
						return nil, err
					}
				}
				if item.Table {
					p.SetStrokeColor(200, 207, 218)
					p.Line(x, y+item.Height, x+cell.Width, y+item.Height)
				}
				x += cell.Width
			}
			if strings.HasPrefix(item.URL, "#") {
				p.AddInternalLink(strings.TrimPrefix(item.URL, "#"), pageLeft+item.Indent, y, bodyWidth-item.Indent, item.Height)
			} else if item.URL != "" {
				p.AddExternalLink(item.URL, pageLeft, y, bodyWidth, item.Height)
			}
			y += item.Height + item.Gap
		}
	}
	if m.missing != 0 {
		return nil, fmt.Errorf("PDF font does not contain U+%04X; use ODT to retain the original text", m.missing)
	}
	b, err := p.GetBytesPdfReturnErr()
	if err != nil {
		return nil, err
	}
	return b, nil
}

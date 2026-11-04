// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package document creates self-contained PDF and editable ODT documents.
// Its pagination and containers derive from the owner's local adscan engine.
package document

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

const odtMIME = "application/vnd.oasis.opendocument.text"
const odtNamespaces = ` xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:fo="urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0" xmlns:xlink="http://www.w3.org/1999/xlink" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0" xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0" office:version="1.3"`

// Document contains presentation data, without database or filesystem access.
type Document struct {
	Title, Subtitle, Language, Creator string
	Date                               time.Time
	Blocks                             []Block
	ContentsTitle, PageLabel           string
	ContentsDepth                      int
}

// Block is a paragraph/heading, table, or embedded PNG. Only one is set.
type Block struct {
	Text, URL, Name string
	Level           int
	Headers         []string
	Rows            [][]string
	Image           []byte
	Chart           *Chart
}

func (d Document) contentsDepth() int {
	if d.ContentsDepth == 0 {
		return 3
	}
	return d.ContentsDepth
}

// Text removes characters that cannot be represented in XML 1.0.
func Text(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF {
			return r
		}
		return '\uFFFD'
	}, s)
}

// Paragraph appends a plain text paragraph.
func (d *Document) Paragraph(s string) { d.Blocks = append(d.Blocks, Block{Text: Text(s)}) }

// Heading appends a heading (levels 1 through 3).
func (d *Document) Heading(level int, s string) {
	d.Blocks = append(d.Blocks, Block{Text: Text(s), Level: level})
}

// Table appends an editable table, retaining complete cell values.
func (d *Document) Table(headers []string, rows [][]string) {
	clean := func(values []string) []string {
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = Text(v)
		}
		return out
	}
	b := Block{Headers: clean(headers)}
	for _, row := range rows {
		b.Rows = append(b.Rows, clean(row))
	}
	d.Blocks = append(d.Blocks, b)
}

// Render returns a complete document. Limits bound pagination and output size.
func Render(ctx context.Context, d Document, format string) ([]byte, error) {
	if format != "pdf" && format != "odt" {
		return nil, fmt.Errorf("unsupported document format %q", format)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Materialize images after the caller has released its database snapshot.
	d.Blocks = append([]Block(nil), d.Blocks...)
	for i := range d.Blocks {
		if d.Blocks[i].Chart != nil {
			image, err := ChartPNG(ctx, *d.Blocks[i].Chart)
			if err != nil {
				return nil, err
			}
			d.Blocks[i].Image = image
		}
	}
	textBytes := len(d.Title) + len(d.Subtitle)
	for _, b := range d.Blocks {
		textBytes += len(b.Text) + len(b.Image)
		if b.Level < 0 || b.Level > 3 || len(b.Headers) > 16 {
			return nil, fmt.Errorf("invalid document block")
		}
		for _, s := range b.Headers {
			textBytes += len(s)
		}
		for _, row := range b.Rows {
			if len(row) != len(b.Headers) {
				return nil, fmt.Errorf("table row has %d cells, want %d", len(row), len(b.Headers))
			}
			for _, s := range row {
				if len(s) > 128*1024 {
					return nil, fmt.Errorf("document cell is too large")
				}
				textBytes += len(s)
			}
		}
		if textBytes > 32<<20 {
			return nil, fmt.Errorf("document exceeds 32 MiB content limit")
		}
	}
	l, err := paginateDocument(ctx, d, d.ContentsTitle, d.PageLabel)
	if err != nil {
		return nil, err
	}
	if len(l.Pages) > 2000 {
		return nil, fmt.Errorf("document exceeds 2000 pages")
	}
	var out []byte
	if format == "pdf" {
		out, err = writePDF(ctx, l)
	} else {
		out, err = writeODT(ctx, l)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(out) > 32<<20 {
		return nil, fmt.Errorf("document exceeds 32 MiB output limit")
	}
	return out, nil
}

func odtEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(Text(s)))
	return b.String()
}

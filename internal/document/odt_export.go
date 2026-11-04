// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Adapted from the local adscan document engine at the owner's request.
package document

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"sort"
	"strings"
)

func odtFontDeclarations() string {
	var b strings.Builder
	b.WriteString(`<office:font-face-decls>`)
	for _, face := range []struct{ name, weight string }{{"Regular", "normal"}, {"Bold", "bold"}} {
		fmt.Fprintf(&b, `<style:font-face style:name="Report%s" svg:font-family="Liberation Sans" svg:font-weight="%s"><svg:font-face-src><svg:font-face-uri xlink:href="Fonts/LiberationSans-%s.ttf" xlink:type="simple"><svg:font-face-format svg:string="truetype"/></svg:font-face-uri></svg:font-face-src></style:font-face>`, face.name, face.weight, face.name)
	}
	b.WriteString(`</office:font-face-decls>`)
	return b.String()
}

func odtItemStyle(item documentItem) string {
	return fmt.Sprintf("P%d_%g_%g_%g", item.Level, item.Size, item.LineHeight, item.Gap)
}

func odtLines(lines []string) string {
	values := make([]string, len(lines))
	for i, s := range lines {
		values[i] = odtEscape(s)
	}
	return strings.Join(values, `<text:line-break/>`)
}

func odtContentsSource(depth int, title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<text:table-of-content text:name="Contents" text:protected="true"><text:table-of-content-source text:outline-level="%d" text:use-outline-level="true"><text:index-title-template text:style-name="IndexTitle">%s</text:index-title-template>`, depth, odtEscape(title))
	for level := 1; level <= depth; level++ {
		fmt.Fprintf(&b, `<text:table-of-content-entry-template text:outline-level="%d" text:style-name="IndexEntry%d"><text:index-entry-link-start/><text:index-entry-text/><text:index-entry-tab-stop style:type="right" style:leader-char="."/><text:index-entry-page-number/><text:index-entry-link-end/></text:table-of-content-entry-template>`, level, level)
	}
	b.WriteString(`</text:table-of-content-source><text:index-body>`)
	return b.String()
}

func odtContentsStyles(depth int) string {
	var b strings.Builder
	for level := 1; level <= depth; level++ {
		item := contentsFormat(level)
		face, weight := "Regular", "normal"
		if item.Bold {
			face, weight = "Bold", "bold"
		}
		fmt.Fprintf(&b, `<style:style style:name="IndexEntry%d" style:family="paragraph"><style:paragraph-properties fo:margin="0pt" fo:margin-left="%gpt" fo:line-height="%gpt" fo:margin-bottom="%gpt" fo:orphans="1" fo:widows="1"><style:tab-stops><style:tab-stop style:type="right" style:position="%gpt" style:leader-text="."/></style:tab-stops></style:paragraph-properties><style:text-properties style:font-name="Report%s" fo:font-size="%gpt" fo:font-weight="%s"/></style:style>`, level, item.Indent, item.LineHeight, item.Gap, bodyWidth-item.Indent, face, item.Size, weight)
	}
	return b.String()
}

func writeODT(ctx context.Context, l documentLayout) ([]byte, error) {
	ns := strings.Replace(odtNamespaces, ` office:version=`, ` xmlns:svg="urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0" office:version=`, 1)
	var content strings.Builder
	content.WriteString(`<?xml version="1.0" encoding="UTF-8"?><office:document-content` + ns + `>` + odtFontDeclarations() + `<office:automatic-styles>`)
	styles := map[string]documentItem{}
	columns := map[int]bool{}
	for _, p := range l.Pages {
		for _, item := range p.Items {
			styles[odtItemStyle(item)] = item
			if item.Table {
				columns[len(item.Cells)] = true
			}
		}
	}
	keys := make([]string, 0, len(styles))
	for key := range styles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, name := range keys {
		item := styles[name]
		weight, face := "normal", "Regular"
		if item.Bold {
			weight, face = "bold", "Bold"
		}
		fmt.Fprintf(&content, `<style:style style:name="%s" style:family="paragraph"><style:paragraph-properties fo:margin="0pt" fo:margin-bottom="%gpt" fo:line-height="%gpt" fo:orphans="1" fo:widows="1"/><style:text-properties style:font-name="Report%s" fo:font-family="Liberation Sans" fo:font-size="%gpt" fo:font-weight="%s"/></style:style>`, name, item.Gap, item.LineHeight, face, item.Size, weight)
	}
	for n := 1; n <= 16; n++ {
		if columns[n] {
			fmt.Fprintf(&content, `<style:style style:name="Column%d" style:family="table-column"><style:table-column-properties style:column-width="%.5fpt"/></style:style>`, n, bodyWidth/float64(n))
		}
	}
	content.WriteString(`<style:style style:name="Grid" style:family="table"><style:table-properties table:align="left" style:width="511.28pt" fo:margin="0pt"/></style:style><style:style style:name="Cell" style:family="table-cell"><style:table-cell-properties fo:padding="0pt" fo:padding-left="4pt" fo:padding-right="4pt" fo:border-bottom="0.4pt solid #c8cfda"/></style:style><style:style style:name="HeaderCell" style:family="table-cell" style:parent-style-name="Cell"><style:table-cell-properties fo:background-color="#e9eef5"/></style:style></office:automatic-styles><office:body><office:text>`)
	content.WriteString(`<text:p text:style-name="CoverTitle">` + odtEscape(l.Document.Title) + `</text:p><text:p text:style-name="CoverDomain">` + odtEscape(l.Document.Subtitle) + `</text:p>`)
	if l.Document.CoverNote != "" {
		content.WriteString(`<text:p text:style-name="CoverDetails">` + odtEscape(l.Document.CoverNote) + `</text:p>`)
	}
	content.WriteString(`<text:p text:style-name="CoverDetails">` + odtEscape(l.Document.Date.UTC().Format("2006-01-02 15:04:05 UTC")) + `</text:p>`)
	tableID := 0
	var pictures []struct {
		name string
		data []byte
	}
	for pageIndex, p := range l.Pages {
		if pageIndex == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content.WriteString(`<text:p text:style-name="PageBreak"/>`)
		if p.Contents {
			if pageIndex == 1 {
				content.WriteString(odtContentsSource(l.Document.contentsDepth(), l.TOCTitle))
				content.WriteString(`<text:index-title text:name="ContentsTitle"><text:p text:style-name="IndexTitle">` + odtLines(p.Items[0].Cells[0].Lines) + `</text:p></text:index-title>`)
			} else {
				content.WriteString(`<text:p text:style-name="IndexTitle">` + odtLines(p.Items[0].Cells[0].Lines) + `</text:p>`)
			}
			for _, item := range p.Items {
				if item.ContentsIndex == 0 {
					continue
				}
				entry := l.Contents[item.ContentsIndex-1]
				fmt.Fprintf(&content, `<text:p text:style-name="IndexEntry%d"><text:a xlink:href="#%s" xlink:type="simple">%s</text:a><text:tab/><text:bookmark-ref text:reference-format="page" text:ref-name="%s">%d</text:bookmark-ref></text:p>`, entry.Level, entry.Anchor, odtLines(item.Cells[0].Lines), entry.Anchor, entry.Page)
			}
			if pageIndex == l.ContentsPages {
				content.WriteString(`</text:index-body></text:table-of-content>`)
			}
			continue
		}
		for _, item := range p.Items {
			if len(item.Image) > 0 {
				name := fmt.Sprintf("Pictures/chart_%d.png", len(pictures)+1)
				pictures = append(pictures, struct {
					name string
					data []byte
				}{name, item.Image})
				fmt.Fprintf(&content, `<text:p><draw:frame draw:name="Chart_%d" text:anchor-type="as-char" svg:width="%gpt" svg:height="%gpt"><draw:image xlink:href="%s" xlink:type="simple" xlink:show="embed" xlink:actuate="onLoad"/></draw:frame></text:p>`, len(pictures), bodyWidth, item.Height, name)
				continue
			}
			if item.Table {
				tableID++
				fmt.Fprintf(&content, `<table:table table:name="Table_%d" table:style-name="Grid"><table:table-column table:style-name="Column%d" table:number-columns-repeated="%d"/><table:table-row>`, tableID, len(item.Cells), len(item.Cells))
				for _, cell := range item.Cells {
					style := "Cell"
					if item.TableHeader {
						style = "HeaderCell"
					}
					lines := append([]string{}, cell.Lines...)
					for len(lines) < int(item.Height/item.LineHeight) {
						lines = append(lines, "")
					}
					fmt.Fprintf(&content, `<table:table-cell table:style-name="%s" office:value-type="string"><text:p text:style-name="%s">%s</text:p></table:table-cell>`, style, odtItemStyle(item), odtLines(lines))
				}
				content.WriteString(`</table:table-row></table:table>`)
				continue
			}
			tag := "text:p"
			attrs := ""
			if item.Level > 0 {
				tag = "text:h"
				attrs = fmt.Sprintf(` text:outline-level="%d"`, item.Level)
			}
			fmt.Fprintf(&content, `<%s text:style-name="%s"%s>`, tag, odtItemStyle(item), attrs)
			if item.Anchor != "" {
				content.WriteString(`<text:bookmark text:name="` + item.Anchor + `"/>`)
			}
			if item.URL != "" {
				content.WriteString(`<text:a xlink:type="simple" xlink:href="` + odtEscape(item.URL) + `">`)
			}
			content.WriteString(odtLines(item.Cells[0].Lines))
			if item.URL != "" {
				content.WriteString(`</text:a>`)
			}
			fmt.Fprintf(&content, `</%s>`, tag)
		}
	}
	content.WriteString(`</office:text></office:body></office:document-content>`)
	stylesXML := `<?xml version="1.0" encoding="UTF-8"?><office:document-styles` + ns + `>` + odtFontDeclarations() + `<office:styles><style:default-style style:family="paragraph"><style:paragraph-properties fo:margin="0pt" fo:line-height="14pt"/><style:text-properties style:font-name="ReportRegular" fo:font-family="Liberation Sans" fo:font-size="10pt" fo:language="` + odtEscape(l.Document.Language) + `"/></style:default-style>
<style:style style:name="CoverTitle" style:family="paragraph" style:master-page-name="Cover"><style:paragraph-properties fo:margin-top="6cm" fo:margin-bottom="14pt" fo:text-align="center" fo:line-height="34pt"/><style:text-properties style:font-name="ReportBold" fo:font-size="24pt" fo:font-weight="bold"/></style:style>
<style:style style:name="CoverDomain" style:family="paragraph"><style:paragraph-properties fo:text-align="center" fo:line-height="23pt"/><style:text-properties fo:font-size="16pt"/></style:style>
<style:style style:name="CoverDetails" style:family="paragraph"><style:paragraph-properties fo:text-align="center" fo:line-height="17pt" fo:margin-top="10pt"/><style:text-properties fo:font-size="12pt"/></style:style>
<style:style style:name="PageBreak" style:family="paragraph" style:master-page-name="Standard"><style:paragraph-properties fo:break-before="page" fo:line-height="1pt" fo:margin="0pt"/><style:text-properties fo:font-size="1pt"/></style:style>
<style:style style:name="Header" style:family="paragraph"><style:paragraph-properties fo:line-height="11pt" fo:margin="0pt"><style:tab-stops><style:tab-stop style:position="255.64pt" style:type="center"/><style:tab-stop style:position="511.28pt" style:type="right"/></style:tab-stops></style:paragraph-properties><style:text-properties fo:font-size="` + fmt.Sprint(l.Header.Size) + `pt"/></style:style>
<style:style style:name="HeaderServer" style:family="text"><style:text-properties style:font-name="ReportBold" fo:font-weight="bold"/></style:style>
<style:style style:name="Footer" style:family="paragraph"><style:paragraph-properties fo:line-height="14pt" fo:margin="0pt"/><style:text-properties fo:font-size="9pt"/></style:style>
<style:style style:name="IndexTitle" style:family="paragraph"><style:paragraph-properties fo:line-height="24pt" fo:margin-bottom="18pt"/><style:text-properties style:font-name="ReportBold" fo:font-size="18pt" fo:font-weight="bold"/></style:style>
` + odtContentsStyles(l.Document.contentsDepth()) + `
</office:styles><office:automatic-styles>
<style:page-layout style:name="CoverLayout"><style:page-layout-properties fo:page-width="595.28pt" fo:page-height="841.89pt" fo:margin="42pt" style:print-orientation="portrait"/></style:page-layout>
<style:page-layout style:name="BodyLayout"><style:page-layout-properties fo:page-width="595.28pt" fo:page-height="841.89pt" fo:margin="42pt" style:print-orientation="portrait"/><style:header-style><style:header-footer-properties fo:min-height="33pt" fo:margin-bottom="17pt"/></style:header-style><style:footer-style><style:header-footer-properties fo:min-height="14pt" fo:margin-top="10pt"/></style:footer-style></style:page-layout>
</office:automatic-styles><office:master-styles><style:master-page style:name="Cover" style:page-layout-name="CoverLayout" style:next-style-name="Standard"/><style:master-page style:name="Standard" style:page-layout-name="BodyLayout"><style:header>`
	stylesXML += `<text:p text:style-name="Header"><text:span text:style-name="HeaderServer">` + odtEscape(l.Header.Values[0]) + `</text:span><text:tab/>` + odtEscape(l.Header.Values[1]) + `<text:tab/>` + odtEscape(l.Header.Values[2]) + `</text:p>`
	stylesXML += `</style:header><style:footer><text:p text:style-name="Footer">` + odtEscape(l.PageLabel) + ` <text:page-number text:select-page="current">2</text:page-number> / <text:page-count>` + fmt.Sprint(len(l.Pages)) + `</text:page-count></text:p></style:footer></style:master-page></office:master-styles></office:document-styles>`
	meta := `<?xml version="1.0" encoding="UTF-8"?><office:document-meta` + ns + `><office:meta><meta:generator>` + odtEscape(l.Document.Creator) + `</meta:generator><dc:title>` + odtEscape(l.Document.Title) + `</dc:title><dc:language>` + odtEscape(l.Document.Language) + `</dc:language></office:meta></office:document-meta>`
	files := []struct {
		name string
		data []byte
	}{{"mimetype", []byte(odtMIME)}, {"content.xml", []byte(content.String())}, {"styles.xml", []byte(stylesXML)}, {"meta.xml", []byte(meta)}}
	files = append(files, pictures...)
	for _, name := range []string{"LiberationSans-Regular.ttf", "LiberationSans-Bold.ttf", "LICENSE.txt"} {
		data, err := documentFonts.ReadFile("fonts/" + name)
		if err != nil {
			return nil, err
		}
		files = append(files, struct {
			name string
			data []byte
		}{"Fonts/" + name, data})
	}
	manifest := `<?xml version="1.0" encoding="UTF-8"?><manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.3"><manifest:file-entry manifest:full-path="/" manifest:version="1.3" manifest:media-type="` + odtMIME + `"/>`
	for _, file := range files[1:] {
		mime := "text/xml"
		if strings.HasSuffix(file.name, ".png") {
			mime = "image/png"
		}
		if strings.HasSuffix(file.name, ".ttf") {
			mime = "application/x-font-ttf"
		}
		if strings.HasSuffix(file.name, ".txt") {
			mime = "text/plain"
		}
		manifest += `<manifest:file-entry manifest:full-path="` + file.name + `" manifest:media-type="` + mime + `"/>`
	}
	manifest += `</manifest:manifest>`
	files = append(files, struct {
		name string
		data []byte
	}{"META-INF/manifest.xml", []byte(manifest)})
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		h.SetMode(0600)
		if file.name == "mimetype" {
			// The ODF signature is a fully populated stored ZIP entry: no
			// extra fields or trailing data descriptor in the MIME signature.
			h.Method = zip.Store
			h.CRC32 = crc32.ChecksumIEEE(file.data)
			h.CompressedSize64 = uint64(len(file.data))
			h.UncompressedSize64 = uint64(len(file.data))
			entry, err := z.CreateRaw(h)
			if err != nil {
				return nil, err
			}
			if _, err = entry.Write(file.data); err != nil {
				return nil, err
			}
		} else {
			entry, err := z.CreateHeader(h)
			if err != nil {
				return nil, err
			}
			if _, err = entry.Write(file.data); err != nil {
				return nil, err
			}
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

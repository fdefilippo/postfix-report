// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package document

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPaginationPreservesLongCellsAndContents(t *testing.T) {
	d := Document{Title: "Prova è", Subtitle: "example.org", Language: "it", Date: time.Unix(0, 0)}
	for i := 0; i < 90; i++ {
		d.Heading(1, fmt.Sprintf("Capitolo %03d", i))
		d.Heading(2, "Sezione è é Ł")
		d.Paragraph("Valori osservati")
	}
	d.Table([]string{"Chiave", "Valore"}, [][]string{{"identificatore", strings.Repeat("0123456789", 1400)}})
	l, err := paginateDocument(context.Background(), d, "Indice", "Pagina")
	if err != nil {
		t.Fatal(err)
	}
	if l.ContentsPages < 2 || len(l.Contents) != 180 {
		t.Fatalf("contents: %d pages, %d entries", l.ContentsPages, len(l.Contents))
	}
	for _, e := range l.Contents {
		found := false
		for _, it := range l.Pages[e.Page-1].Items {
			if it.Anchor == e.Anchor {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing destination: %+v", e)
		}
	}
	var digits strings.Builder
	headers := 0
	for _, p := range l.Pages {
		height := 0.0
		for _, it := range p.Items {
			height += it.Height + it.Gap
			if it.TableHeader {
				headers++
			}
			if it.Table && !it.TableHeader {
				for _, c := range it.Cells {
					for _, s := range c.Lines {
						digits.WriteString(s)
					}
				}
			}
		}
		if height > bodyBottom-bodyTop+18 {
			t.Fatalf("page overflow %f", height)
		}
	}
	if headers < 2 || strings.Count(digits.String(), "0123456789") < 1390 {
		t.Fatal("long row was dropped or headers not repeated")
	}
}

func TestSelfContainedContainersAndPDFText(t *testing.T) {
	d := Document{Title: "Test è & <report>", Subtitle: "example.org", Language: "it", Creator: "postfix-report test", ContentsTitle: "Indice", PageLabel: "Pagina", Date: time.Unix(1000, 0)}
	d.Heading(1, "Traffico")
	d.Paragraph("Destinatari osservati: 42")
	d.Table([]string{"Dominio", "Messaggi"}, [][]string{{"example.org", "42"}})
	v := 2.0
	d.Blocks = append(d.Blocks, Block{Chart: &Chart{Type: "bar", Labels: []string{"00", "01"}, Status: []string{"missing", "partial"}, Series: []Series{{Name: "Messaggi", Color: "#2f6fd6", Values: []*float64{nil, &v}}}}})
	odt, err := Render(context.Background(), d, "odt")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(odt), int64(len(odt)))
	if err != nil {
		t.Fatal(err)
	}
	if z.File[0].Name != "mimetype" || z.File[0].Method != zip.Store {
		t.Fatal("ODF MIME signature")
	}
	found := map[string]bool{}
	for _, f := range z.File {
		found[f.Name] = true
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(f.Name, ".xml") {
			dec := xml.NewDecoder(bytes.NewReader(b))
			for {
				_, err := dec.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("%s: %v", f.Name, err)
				}
			}
		}
		if f.Name == "content.xml" && !bytes.Contains(b, []byte("example.org")) {
			t.Fatal("table omitted")
		}
	}
	for _, name := range []string{"Pictures/chart_1.png", "Fonts/LiberationSans-Regular.ttf", "Fonts/LiberationSans-Bold.ttf", "META-INF/manifest.xml"} {
		if !found[name] {
			t.Error("missing embedded", name)
		}
	}
	pdf, err := Render(context.Background(), d, "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	if bin, err := exec.LookPath("pdftotext"); err == nil {
		p := filepath.Join(t.TempDir(), "report.pdf")
		if err = os.WriteFile(p, pdf, 0600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bin, p, "-").CombinedOutput()
		if err != nil {
			t.Fatalf("pdftotext: %v %s", err, out)
		}
		for _, value := range []string{"Traffico", "Destinatari osservati: 42", "example.org"} {
			if !strings.Contains(string(out), value) {
				t.Errorf("PDF missing %q: %s", value, out)
			}
		}
	} else {
		t.Log("pdftotext not available; container checked")
	}
}

func TestCancellationAndUnsupportedGlyph(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, Document{}, "pdf"); err == nil {
		t.Fatal("cancelled render succeeded")
	}
	d := Document{Title: "Test", Subtitle: "example.org", ContentsTitle: "Contents", PageLabel: "Page"}
	d.Paragraph("Unsupported: \U0001f9ea")
	if _, err := Render(context.Background(), d, "pdf"); err == nil {
		t.Fatal("unsupported glyph silently lost")
	}
	if _, err := Render(context.Background(), d, "odt"); err != nil {
		t.Fatal("ODT must retain Unicode", err)
	}
}

// A deterministic cancellation during wrapping proves that even a single long
// identifier is interruptible, rather than only checking between table rows.
type wrapContext struct {
	context.Context
	checks int
}

func (c *wrapContext) Err() error {
	c.checks++
	if c.checks > 20 {
		return context.Canceled
	}
	return nil
}
func TestLongCellWrappingHonorsCancellation(t *testing.T) {
	ctx := &wrapContext{Context: context.Background()}
	m, err := newDocumentMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.wrap(strings.Repeat("a", 128*1024), 50, 10, false); err != context.Canceled {
		t.Fatalf("long cell did not stop: %v", err)
	}
}

func TestHeadingStaysWithChart(t *testing.T) {
	d := Document{Title: "Test", ContentsTitle: "Indice"}
	d.Paragraph(strings.Repeat("body\n", 42))
	d.Heading(2, "Chart title")
	d.Paragraph("Description")
	d.Blocks = append(d.Blocks, Block{Image: []byte("layout placeholder")})
	l, err := paginateDocument(context.Background(), d, "Indice", "Pagina")
	if err != nil {
		t.Fatal(err)
	}
	titlePage, imagePage := -1, -1
	for i, p := range l.Pages {
		for _, item := range p.Items {
			if item.Anchor != "" {
				titlePage = i
			}
			if len(item.Image) > 0 {
				imagePage = i
			}
		}
	}
	if titlePage < 0 || titlePage != imagePage {
		t.Fatalf("orphaned chart title on %d, image on %d", titlePage, imagePage)
	}
}

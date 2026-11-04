// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package document

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Chart is the static equivalent of a dashboard chart. Nil values are gaps.
type Chart struct {
	Type    string   `json:"type"`
	Labels  []string `json:"labels"`
	Short   []string `json:"short"`
	Status  []string `json:"status"`
	Unit    string   `json:"unit"`
	Stacked bool     `json:"stacked"`
	Series  []Series `json:"series"`
}

// Series contains already computed values, retaining missing-data markers.
type Series struct {
	Name   string     `json:"name"`
	Color  string     `json:"color"`
	Values []*float64 `json:"values"`
}

// ChartPNG draws a high resolution chart using the embedded document font.
func ChartPNG(ctx context.Context, c Chart) ([]byte, error) {
	const w, h = 1500, 660
	const left, right, top, bottom = 135, 1470, 100, 535
	n := len(c.Labels)
	if n == 0 || n > 366 || len(c.Series) > 16 {
		return nil, fmt.Errorf("invalid chart size")
	}
	data, err := documentFonts.ReadFile("fonts/LiberationSans-Regular.ttf")
	if err != nil {
		return nil, err
	}
	f, err := opentype.Parse(data)
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 21, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	defer face.Close()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(im, im.Bounds(), image.White, image.Point{}, draw.Src)
	ink := color.RGBA{25, 35, 45, 255}
	text := func(x, y int, s string) error {
		for _, r := range s {
			if _, ok := face.GlyphAdvance(r); !ok {
				return fmt.Errorf("chart font does not contain U+%04X", r)
			}
		}
		d := font.Drawer{Dst: im, Src: image.NewUniform(ink), Face: face, Dot: fixed.P(x, y)}
		d.DrawString(s)
		return nil
	}
	rect := func(x0, y0, x1, y1 int, col color.Color) {
		draw.Draw(im, image.Rect(x0, y0, x1, y1), image.NewUniform(col), image.Point{}, draw.Src)
	}
	line := func(x0, y0, x1, y1 int, col color.Color) {
		steps := max(abs(x1-x0), abs(y1-y0))
		if steps == 0 {
			steps = 1
		}
		for i := 0; i <= steps; i++ {
			x := x0 + (x1-x0)*i/steps
			y := y0 + (y1-y0)*i/steps
			rect(x-1, y-1, x+2, y+2, col)
		}
	}
	colors := make([]color.RGBA, len(c.Series))
	maximum := 0.0
	for si, s := range c.Series {
		if len(s.Values) != n {
			return nil, fmt.Errorf("invalid chart series length")
		}
		var rgb uint64
		rgb, err = strconv.ParseUint(strings.TrimPrefix(s.Color, "#"), 16, 24)
		if err != nil {
			return nil, err
		}
		colors[si] = color.RGBA{uint8(rgb >> 16), uint8(rgb >> 8), uint8(rgb), 255}
		for _, v := range s.Values {
			if v != nil {
				if math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 {
					return nil, fmt.Errorf("invalid chart value")
				}
				maximum = max(maximum, *v)
			}
		}
	}
	if c.Stacked {
		for i := 0; i < n; i++ {
			sum := 0.0
			for _, s := range c.Series {
				if s.Values[i] != nil {
					sum += *s.Values[i]
				}
			}
			maximum = max(maximum, sum)
		}
	}
	if maximum <= 0 {
		maximum = 1
	}
	maximum *= 1.08
	step := float64(right-left) / float64(n)
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		x0, x1 := left+int(float64(i)*step), left+int(float64(i+1)*step)
		status := ""
		if i < len(c.Status) {
			status = c.Status[i]
		}
		if status == "missing" || status == "nonexistent" {
			rect(x0, top, x1, bottom, color.RGBA{235, 237, 240, 255})
			for y := top; y < bottom; y += 18 {
				line(x0, y, min(x1, x0+12), min(bottom, y+12), color.RGBA{180, 186, 195, 255})
			}
		} else if status == "partial" {
			rect(x0, top, x1, bottom, color.RGBA{255, 248, 225, 255})
		}
	}
	for i := 0; i <= 4; i++ {
		y := bottom - (bottom-top)*i/4
		line(left, y, right, y, color.RGBA{210, 215, 222, 255})
		if err := text(5, y+7, strconv.FormatFloat(maximum*float64(i)/4, 'g', 4, 64)); err != nil {
			return nil, err
		}
	}
	yof := func(v float64) int { return bottom - int(v/maximum*float64(bottom-top)) }
	for si, s := range c.Series {
		prevX, prevY, pen := 0, 0, false
		for i, v := range s.Values {
			if v == nil {
				pen = false
				continue
			}
			x := left + int((float64(i)+0.5)*step)
			y := yof(*v)
			if c.Type == "line" {
				if pen {
					line(prevX, prevY, x, y, colors[si])
				}
				rect(x-3, y-3, x+4, y+4, colors[si])
				prevX, prevY, pen = x, y, true
			} else if c.Stacked {
				base := 0.0
				for j := 0; j < si; j++ {
					if c.Series[j].Values[i] != nil {
						base += *c.Series[j].Values[i]
					}
				}
				x0 := left + int(float64(i)*step) + min(2, int(step/4))
				x1 := left + int(float64(i+1)*step) - min(2, int(step/4))
				rect(x0, yof(base+*v), max(x0+1, x1), yof(base), colors[si])
			} else {
				bw := step * .85 / float64(max(1, len(c.Series)))
				x0 := left + int((float64(i)+.075)*step+float64(si)*bw)
				rect(x0, y, max(x0+1, x0+int(bw)-1), bottom, colors[si])
			}
		}
	}
	for i := 0; i < n; i++ {
		if n > 16 && i%((n+15)/16) != 0 {
			continue
		}
		label := c.Labels[i]
		if len(c.Short) == n {
			label = c.Short[i]
		}
		if err := text(left+int(float64(i)*step), bottom+30, label); err != nil {
			return nil, err
		}
	}
	for i, s := range c.Series {
		x, y := left+(i%3)*440, 30+(i/3)*30
		rect(x, y-15, x+24, y+4, colors[i])
		if err := text(x+32, y+2, s.Name); err != nil {
			return nil, err
		}
	}
	if err := text(left, h-15, c.Unit); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

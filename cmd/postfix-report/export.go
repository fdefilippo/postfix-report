// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/exporter"
	"github.com/fdefilippo/postfix-report/internal/period"
	"github.com/fdefilippo/postfix-report/internal/web"
)

func (c *cli) serveCmd(ctx context.Context, path string, overrides map[string]string) int {
	load := func() (*config.Config, error) {
		cfg, err := config.Load(path)
		if err != nil {
			return nil, err
		}
		if v, ok := overrides["listen"]; ok {
			cfg.Web.Listen = v
		}
		if v, ok := overrides["prefix"]; ok {
			cfg.Web.Prefix = v
		}
		if v, ok := overrides["origin"]; ok {
			cfg.Web.PublicOrigin = v
		}
		return cfg, cfg.Finish()
	}
	cfg, err := load()
	if err == nil {
		err = web.Run(ctx, cfg, load, version, c.log)
	}
	if err != nil {
		c.log.Error("serve", "error", err)
		return exitError
	}
	return exitOK
}

func (c *cli) exportCmd(ctx context.Context, s *selection, format, output string) int {
	if (format != "pdf" && format != "odt") || output == "" || filepath.Ext(output) != "."+format {
		fmt.Fprintln(c.stderr, "export: specificare --format pdf|odt e --output FILE con estensione corrispondente")
		return exitUsage
	}
	n := 0
	var p period.Period
	for k, key := range map[period.Kind]string{period.Day: s.date, period.Week: s.week, period.Month: s.month, period.Year: s.year} {
		if key != "" {
			var err error
			p, err = period.Parse(k, key, c.cfg.Location)
			if err != nil {
				fmt.Fprintln(c.stderr, err)
				return exitUsage
			}
			n++
		}
	}
	if n != 1 {
		fmt.Fprintln(c.stderr, "export: specificare un solo periodo")
		return exitUsage
	}
	if _, err := os.Lstat(output); err == nil {
		fmt.Fprintln(c.stderr, "export: il file di destinazione esiste già")
		return exitError
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(c.stderr, err)
		return exitError
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	b, err := exporter.Render(ctx, c.cfg, p, format, version)
	if err == nil {
		err = writeNewDocument(output, b)
	}
	if err != nil {
		c.log.Error("export", "error", err)
		return exitError
	}
	fmt.Fprintln(c.stdout, "documento esportato:", output)
	return exitOK
}

// Publish a complete file without replacing an existing target or a symlink.
func writeNewDocument(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".postfix-report-export-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Link(f.Name(), path)
}

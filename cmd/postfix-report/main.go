// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

// Command postfix-report analyzes Postfix logs, keeps history in SQLite and
// generates static HTML reports.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	// Embedded zoneinfo: input.timezone works on systems without tzdata
	// (minimal containers, stripped installs).
	_ "time/tzdata"

	"github.com/fdefilippo/postfix-report/internal/app"
	"github.com/fdefilippo/postfix-report/internal/config"
)

// Exit codes.
const (
	exitOK        = 0
	exitError     = 1 // runtime failure
	exitUsage     = 2 // bad command line or configuration
	exitLocked    = 3 // another instance is running
	exitVerify    = 4 // verify found inconsistencies
	exitPartial   = 5 // some input files could not be imported
	defaultConfig = "/etc/postfix-report/config.yaml"
)

var version = "dev"

const usage = `postfix-report — statistiche Postfix con report HTML statici

Uso:
  postfix-report [-config FILE] <comando> [opzioni]

Comandi:
  import   --file F [--file F2 ...] | --directory D   importa log (anche .gz)
  generate [--all | --date D | --week YYYY-Www | --month YYYY-MM | --year YYYY]
           senza opzioni rigenera le pagine invalidate
  update   importa i percorsi configurati e rigenera solo i report interessati
  rebuild  ricalcola correlazioni e aggregati dagli eventi e rigenera tutto
  verify   controlla integrità e coerenza di database e statistiche
  stats    [--from D --to D] [--json]  stampa le metriche di un intervallo
  check-config                         valida la configurazione
  version

Opzioni globali:
  -config FILE   file di configurazione (default ` + defaultConfig + `,
                 oppure variabile POSTFIX_REPORT_CONFIG)

Codici di uscita: 0 ok, 1 errore, 2 uso/configurazione, 3 già in esecuzione,
4 verify fallita, 5 import parziale (alcuni file non leggibili).
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func run(args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("postfix-report", flag.ContinueOnError)
	global.SetOutput(stderr)
	global.Usage = func() { fmt.Fprint(stderr, usage) }
	cfgPath := global.String("config", "", "configuration file")
	if err := global.Parse(args); err != nil {
		return exitUsage
	}
	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, cargs := rest[0], rest[1:]
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, "postfix-report", version)
		return exitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	path := *cfgPath
	if path == "" {
		path = os.Getenv("POSTFIX_REPORT_CONFIG")
	}
	if path == "" {
		path = defaultConfig
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "configurazione non valida:", err)
		return exitUsage
	}
	log := newLogger(cfg.Logging.Level, stderr)
	if cmd == "check-config" {
		fmt.Fprintln(stdout, "configurazione valida:", path)
		return exitOK
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := &cli{cfg: cfg, log: log, stdout: stdout, stderr: stderr}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var handler func(context.Context, *app.App) int
	switch cmd {
	case "import":
		var files stringList
		fs.Var(&files, "file", "log file to import (repeatable)")
		dir := fs.String("directory", "", "import all regular files of a directory")
		if fs.Parse(cargs) != nil {
			return exitUsage
		}
		if len(files) == 0 && *dir == "" {
			fmt.Fprintln(stderr, "import: specificare --file o --directory")
			return exitUsage
		}
		handler = func(ctx context.Context, a *app.App) int { return c.importCmd(ctx, a, files, *dir) }
	case "generate":
		sel := &selection{}
		fs.BoolVar(&sel.all, "all", false, "regenerate every page")
		fs.StringVar(&sel.date, "date", "", "daily report YYYY-MM-DD")
		fs.StringVar(&sel.week, "week", "", "weekly report YYYY-Www")
		fs.StringVar(&sel.month, "month", "", "monthly report YYYY-MM")
		fs.StringVar(&sel.year, "year", "", "yearly report YYYY")
		if fs.Parse(cargs) != nil {
			return exitUsage
		}
		handler = func(ctx context.Context, a *app.App) int { return c.generateCmd(ctx, a, sel) }
	case "update":
		if fs.Parse(cargs) != nil {
			return exitUsage
		}
		handler = c.updateCmd
	case "rebuild":
		if fs.Parse(cargs) != nil {
			return exitUsage
		}
		handler = c.rebuildCmd
	case "verify":
		if fs.Parse(cargs) != nil {
			return exitUsage
		}
		handler = c.verifyCmd
	case "stats":
		from := fs.String("from", "", "first day YYYY-MM-DD (default: first day with data)")
		to := fs.String("to", "", "last day YYYY-MM-DD (default: last day with data)")
		asJSON := fs.Bool("json", false, "JSON output")
		if fs.Parse(cargs) != nil {
			return exitUsage
		}
		handler = func(ctx context.Context, a *app.App) int { return c.statsCmd(ctx, a, *from, *to, *asJSON) }
	default:
		fmt.Fprintf(stderr, "comando sconosciuto %q\n\n%s", cmd, usage)
		return exitUsage
	}
	a, err := app.Open(cfg, log)
	if errors.Is(err, app.ErrLocked) {
		log.Warn("skipped: another postfix-report process holds the lock", "lock", cfg.Database.Path+".lock")
		return exitLocked
	}
	if err != nil {
		log.Error("cannot open database", "err", err)
		return exitError
	}
	defer a.Close()
	return handler(ctx, a)
}

func newLogger(level string, w io.Writer) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l}))
}

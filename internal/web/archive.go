// Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/fdefilippo/postfix-report/internal/config"
	"github.com/fdefilippo/postfix-report/internal/exporter"
)

func (s *Server) previewArchive(w http.ResponseWriter, r *http.Request) {
	cfg, req := s.decodeExportRequest(w, r)
	if req == nil {
		return
	}
	if req.Archive == nil {
		http.Error(w, "expected archive selection", http.StatusBadRequest)
		return
	}
	plan, err := exporter.PreviewArchive(r.Context(), cfg, *req.Archive, s.version)
	if err != nil {
		s.log.Warn("archive preview failed", "error", err)
		http.Error(w, "archive unavailable: check dates and run update", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

var archiveTemplate = template.Must(template.New("archive").Funcs(template.FuncMap{
	"t": func(it, en string) string { return it },
}).Parse(`<section class="card noprint" id="document-export" data-archive="true" data-api="{{.Web.Prefix}}api/" data-lang="{{.Output.Language}}">
<h2>{{t "Esporta archivio completo" "Export complete archive"}}</h2>
<p>{{t "Un unico PDF o ODT con indice navigabile e tutti i report selezionati." "One PDF or ODT with a navigable index and all selected reports."}}</p>
<p><label><input type="checkbox" data-all checked> {{t "Tutto l'archivio" "Entire archive"}}</label>
<label>{{t "Dal" "From"}} <input type="date" data-from disabled></label>
<label>{{t "Al" "To"}} <input type="date" data-to disabled></label></p>
<fieldset><legend>{{t "Tipi di report" "Report types"}}</legend>
{{if .Reports.Yearly}}<label><input type="checkbox" data-kind="year" checked> {{t "Annuali" "Yearly"}}</label>{{end}}
{{if .Reports.Monthly}}<label><input type="checkbox" data-kind="month" checked> {{t "Mensili" "Monthly"}}</label>{{end}}
{{if .Reports.Weekly}}<label><input type="checkbox" data-kind="week" checked> {{t "Settimanali" "Weekly"}}</label>{{end}}
{{if .Reports.Daily}}<label><input type="checkbox" data-kind="day" checked> {{t "Giornalieri" "Daily"}}</label>{{end}}</fieldset>
<p class="note">{{t "Sono inclusi i periodi interi che intersecano le date scelte. I conteggi dei diversi periodi non vanno sommati." "Whole calendar periods overlapping the chosen dates are included. Counts from different periods must not be added together."}}</p>
<p class="note">{{t "Limiti per documento: 200 report, 2000 pagine, 32 MiB. Gli archivi oltre i limiti vengono rifiutati, mai troncati." "Limits per document: 200 reports, 2000 pages, 32 MiB. Archives exceeding the limits are rejected, never truncated."}}</p>
<p><button type="button" data-preview>{{t "Verifica selezione" "Check selection"}}</button>
<button type="button" data-export="pdf" disabled>PDF</button> <button type="button" data-export="odt" disabled>ODT</button></p>
<p data-plan aria-live="polite"></p><p role="status" aria-live="polite"></p>
<noscript>{{t "Per esportare dal browser è necessario JavaScript." "Browser exports require JavaScript."}}</noscript>
</section><script src="{{.Web.Prefix}}api/export.js" defer></script>`))

func archivePanel(cfg *config.Config) string {
	t, err := archiveTemplate.Clone()
	if err != nil {
		return ""
	}
	t.Funcs(template.FuncMap{"t": func(it, en string) string {
		if cfg.Output.Language == "en" {
			return en
		}
		return it
	}})
	var b bytes.Buffer
	if err = t.Execute(&b, cfg); err != nil {
		return ""
	}
	return b.String()
}

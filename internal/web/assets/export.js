/* Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
 * SPDX-License-Identifier: GPL-3.0-or-later */
(function () {
  "use strict";
  var panel = document.getElementById("document-export");
  if (!panel) return;
  var en = panel.dataset.lang === "en";
  function L(it, english) { return en ? english : it; }
  var status = panel.querySelector("[role=status]");
  var buttons = panel.querySelectorAll("[data-export]");
  var api = panel.dataset.api;
  var archive = panel.dataset.archive === "true";
  var checked = "", revision = 0, busy = false, checking = false;
  var all = panel.querySelector("[data-all]");
  var preview = panel.querySelector("[data-preview]");
  var planText = panel.querySelector("[data-plan]");
  function json(url, options) {
    return fetch(url, options).then(function (r) {
      if (!r.ok) {
        if (r.status === 429) throw new Error(L("Servizio occupato: riprovare fra poco.", "Service busy: retry shortly."));
        if (r.status === 400 || r.status === 409) throw new Error(L("Selezione non disponibile: verificare date e tipi di report o aggiornare i dati.", "Selection unavailable: check dates and report types or update the data."));
        throw new Error(L("Esportazione non disponibile. Riprovare dopo l’aggiornamento.", "Export unavailable. Retry after updating."));
      }
      return r.json();
    });
  }
  function post(path, body) {
    return json(api + "session").then(function (session) {
      return json(api + path, { method: "POST", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf }, body: JSON.stringify(body) });
    });
  }
  function selection() {
    var kinds = Array.from(panel.querySelectorAll("[data-kind]:checked")).map(function (input) { return input.dataset.kind; });
    var from = all.checked ? "" : panel.querySelector("[data-from]").value;
    var to = all.checked ? "" : panel.querySelector("[data-to]").value;
    if (!kinds.length || (!all.checked && (!from || !to || from > to))) {
      throw new Error(L("Selezionare almeno un tipo di report e un intervallo valido.", "Select at least one report type and a valid date range."));
    }
    return { from: from, to: to, kinds: kinds };
  }
  function refresh() {
    if (archive) {
      panel.querySelectorAll("input").forEach(function (input) { input.disabled = busy; });
      panel.querySelector("[data-from]").disabled = busy || all.checked;
      panel.querySelector("[data-to]").disabled = busy || all.checked;
      preview.disabled = busy || checking;
    }
    buttons.forEach(function (button) { button.disabled = busy || (archive && !checked); });
  }
  function checkSelection() {
    var sel;
    var current = ++revision;
    checked = "";
    try { sel = selection(); } catch (error) { status.textContent = error.message; checking = false; refresh(); return; }
    checking = true;
    status.textContent = L(" Verifica della selezione…", " Checking selection…");
    refresh();
    post("archives/preview", { archive: sel, format: "pdf" }).then(function (plan) {
      if (current !== revision) return;
      var names = { year: L("annuali", "yearly"), month: L("mensili", "monthly"), week: L("settimanali", "weekly"), day: L("giornalieri", "daily") };
      var counts = sel.kinds.map(function (kind) { return (plan.counts[kind] || 0) + " " + names[kind]; });
      planText.textContent = plan.from + " — " + plan.to + ": " + plan.total + " report (" + counts.join(", ") + ").";
      if (plan.allowed) {
        checked = JSON.stringify(sel);
        status.textContent = L("Selezione pronta. Scegliere PDF oppure ODT.", "Selection ready. Choose PDF or ODT.");
      } else {
        status.textContent = L("Troppi report: ridurre l’intervallo o i tipi selezionati. Limite: ", "Too many reports: reduce the date range or selected types. Limit: ") + plan.limit + ".";
      }
    }).catch(function (error) { if (current === revision) status.textContent = error.message; })
      .finally(function () { if (current === revision) { checking = false; refresh(); } });
  }
  function poll(id, deadline) {
    return json(api + "exports/" + id).then(function (job) {
      if (job.state === "failed") throw new Error(job.error);
      if (job.state === "ready") {
        var a = document.createElement("a");
        a.href = api + "exports/" + id + "/download";
        a.textContent = L("Scarica il documento", "Download the document");
        status.textContent = " "; status.appendChild(a); a.click();
        return;
      }
      if (Date.now() > deadline) throw new Error(L("Tempo di attesa scaduto. Riprovare.", "Export wait expired. Please retry."));
      return new Promise(function (resolve) { setTimeout(resolve, 700); }).then(function () { return poll(id, deadline); });
    });
  }
  buttons.forEach(function (button) {
    button.addEventListener("click", function () {
      var body = { kind: panel.dataset.kind, key: panel.dataset.key, format: button.dataset.export };
      if (archive) {
        var sel;
        try { sel = selection(); } catch (error) { status.textContent = error.message; return; }
        if (JSON.stringify(sel) !== checked) { checkSelection(); return; }
        body = { archive: sel, format: button.dataset.export };
      }
      busy = true; refresh();
      status.textContent = L(" Preparazione del documento…", " Preparing document…");
      post("exports", body).then(function (job) { return poll(job.id, Date.now() + 11 * 60 * 1000); })
        .catch(function (error) { status.textContent = " " + error.message; })
        .finally(function () { busy = false; refresh(); });
    });
  });
  if (archive) {
    preview.addEventListener("click", checkSelection);
    panel.querySelectorAll("input").forEach(function (input) {
      input.addEventListener("change", function () {
        ++revision; checking = false; checked = ""; planText.textContent = "";
        status.textContent = L("Verificare la nuova selezione prima di esportare.", "Check the new selection before exporting.");
        refresh();
      });
    });
    checkSelection();
  }
}());

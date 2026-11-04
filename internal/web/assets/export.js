/* Copyright (C) 2026 Francesco Defilippo <francesco@defilippo.org>
 * SPDX-License-Identifier: GPL-3.0-or-later */
(function () {
  "use strict";
  var panel = document.getElementById("document-export");
  if (!panel) return;
  var en = panel.dataset.lang === "en";
  function L(it, english) { return en ? english : it; }
  var status = panel.querySelector("[role=status]");
  var buttons = panel.querySelectorAll("button");
  var api = panel.dataset.api;
  function json(url, options) {
    return fetch(url, options).then(function (r) {
      if (!r.ok) {
        if (r.status === 429) throw new Error(L("Servizio occupato: riprovare fra poco.", "Service busy: retry shortly."));
        throw new Error(L("Esportazione non disponibile. Riprovare dopo l’aggiornamento.", "Export unavailable. Retry after updating."));
      }
      return r.json();
    });
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
      buttons.forEach(function (b) { b.disabled = true; });
      status.textContent = L(" Preparazione del documento…", " Preparing document…");
      json(api + "session").then(function (session) {
        return json(api + "exports", { method: "POST", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.csrf },
          body: JSON.stringify({ kind: panel.dataset.kind, key: panel.dataset.key, format: button.dataset.export }) });
      }).then(function (job) { return poll(job.id, Date.now() + 11 * 60 * 1000); })
        .catch(function (error) { status.textContent = " " + error.message; })
        .finally(function () { buttons.forEach(function (b) { b.disabled = false; }); });
    });
  });
}());

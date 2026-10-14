/* postfix-report — offline charts and tables. No dependencies, no network.
 * Chart data is embedded in each page as <script type="application/json">,
 * so pages work from file:// as well as from a static web server. */
(function () {
  "use strict";

  var NS = "http://www.w3.org/2000/svg";
  var nf = new Intl.NumberFormat("it-IT", { maximumFractionDigits: 2 });

  function el(name, attrs, parent) {
    var e = document.createElementNS(NS, name);
    for (var k in attrs) if (Object.prototype.hasOwnProperty.call(attrs, k)) e.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(e);
    return e;
  }

  function fmt(v, unit) {
    if (v === null || v === undefined) return "n/d";
    if (unit === "bytes") return fmtBytes(v);
    if (unit === "s") return fmtSeconds(v);
    return nf.format(v);
  }

  function fmtBytes(v) {
    var u = ["B", "kB", "MB", "GB", "TB"], i = 0;
    while (Math.abs(v) >= 1000 && i < u.length - 1) { v /= 1000; i++; }
    return nf.format(Math.round(v * 10) / 10) + " " + u[i];
  }

  function fmtSeconds(v) {
    if (v < 60) return nf.format(Math.round(v * 100) / 100) + " s";
    if (v < 3600) return nf.format(Math.round(v / 6) / 10) + " min";
    if (v < 86400) return nf.format(Math.round(v / 360) / 10) + " h";
    return nf.format(Math.round(v / 8640) / 10) + " g";
  }

  function niceMax(v) {
    if (v <= 0) return 1;
    var p = Math.pow(10, Math.floor(Math.log10(v)));
    var n = v / p;
    var step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10;
    return step * p;
  }

  var STATUS_TEXT = {
    complete: "copertura completa",
    partial: "copertura parziale",
    missing: "dati assenti",
    nonexistent: "ora inesistente (cambio ora legale)"
  };

  /* ---------- tooltip ---------- */
  var tip;
  function showTip(html, x, y) {
    if (!tip) {
      tip = document.createElement("div");
      tip.className = "tooltip";
      tip.setAttribute("role", "tooltip");
      document.body.appendChild(tip);
    }
    tip.innerHTML = html;
    tip.style.display = "block";
    var r = tip.getBoundingClientRect();
    var left = Math.min(x + 14, window.innerWidth - r.width - 8);
    var top = y + 14;
    if (top + r.height > window.innerHeight - 8) top = y - r.height - 14;
    tip.style.left = Math.max(8, left) + window.scrollX + "px";
    tip.style.top = Math.max(8, top) + window.scrollY + "px";
  }
  function hideTip() { if (tip) tip.style.display = "none"; }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  /* ---------- charts ---------- */
  function Chart(container, spec) {
    this.c = container;
    this.spec = spec;
    this.hidden = {};
    this.legend();
    this.draw();
    var self = this, t;
    window.addEventListener("resize", function () {
      clearTimeout(t);
      t = setTimeout(function () { self.draw(); }, 120);
    });
  }

  Chart.prototype.legend = function () {
    var self = this, spec = this.spec;
    var lg = document.createElement("div");
    lg.className = "legend";
    spec.series.forEach(function (s, i) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "legend-item";
      b.setAttribute("aria-pressed", "true");
      b.innerHTML = '<span class="swatch" style="background:' + esc(s.color) + '"></span>' + esc(s.name);
      b.title = "Mostra/nascondi la serie";
      b.addEventListener("click", function () {
        self.hidden[i] = !self.hidden[i];
        b.setAttribute("aria-pressed", self.hidden[i] ? "false" : "true");
        b.classList.toggle("off", !!self.hidden[i]);
        self.draw();
      });
      lg.appendChild(b);
    });
    if (spec.weekend) {
      var we = document.createElement("span");
      we.className = "legend-note";
      we.innerHTML = '<span class="swatch weekend"></span>sabato e domenica';
      lg.appendChild(we);
    }
    if (spec.hasMissing) {
      var m = document.createElement("span");
      m.className = "legend-note";
      m.innerHTML = '<span class="swatch hatch"></span>dati assenti';
      lg.appendChild(m);
    }
    this.c.parentNode.insertBefore(lg, this.c);
  };

  Chart.prototype.visible = function () {
    var self = this;
    return this.spec.series.map(function (s, i) { return { s: s, i: i }; })
      .filter(function (x) { return !self.hidden[x.i]; });
  };

  Chart.prototype.draw = function () {
    var spec = this.spec, c = this.c;
    // In a hidden tab the width is unknown: keep the current drawing, the
    // chart is redrawn when its tab is shown.
    if (!c.clientWidth && c.firstChild) return;
    c.innerHTML = "";
    var W = Math.max(c.clientWidth || 600, 280), H = spec.height || 260;
    var m = { l: 56, r: 12, t: 12, b: spec.labels.length > 16 ? 44 : 30 };
    var svg = el("svg", { width: W, height: H, viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": spec.title || "grafico" }, c);
    var vis = this.visible();
    var n = spec.labels.length;
    var maxV = 0;
    vis.forEach(function (x) {
      x.s.values.forEach(function (v) { if (v !== null && v > maxV) maxV = v; });
    });
    if (spec.stacked) {
      maxV = 0;
      for (var k = 0; k < n; k++) {
        var sum = 0;
        vis.forEach(function (x) { if (x.s.values[k] !== null) sum += x.s.values[k]; });
        if (sum > maxV) maxV = sum;
      }
    }
    var top = niceMax(maxV);
    var pw = W - m.l - m.r, ph = H - m.t - m.b;
    var y = function (v) { return m.t + ph - (v / top) * ph; };
    // grid and axis
    for (var g = 0; g <= 4; g++) {
      var gv = top * g / 4, gy = y(gv);
      el("line", { x1: m.l, x2: W - m.r, y1: gy, y2: gy, class: "grid" }, svg);
      var tx = el("text", { x: m.l - 6, y: gy + 4, class: "axis", "text-anchor": "end" }, svg);
      tx.textContent = fmt(gv, spec.unit);
    }
    var bw = pw / n;
    var every = Math.ceil(n / Math.max(1, Math.floor(pw / 34)));
    var self = this;
    var linePts = {};
    for (var i = 0; i < n; i++) {
      var x0 = m.l + i * bw;
      var st = spec.status ? spec.status[i] : "complete";
      if (spec.weekend && spec.weekend[i]) {
        el("rect", { x: x0, y: m.t, width: bw, height: ph, class: "weekend" }, svg);
      }
      if (st === "missing" || st === "nonexistent") {
        el("rect", { x: x0 + 1, y: m.t, width: Math.max(bw - 2, 1), height: ph, class: "missing" }, svg);
      } else if (st === "partial") {
        el("rect", { x: x0 + 1, y: m.t, width: Math.max(bw - 2, 1), height: ph, class: "partial" }, svg);
      }
      if (i % every === 0) {
        var lt = el("text", { x: x0 + bw / 2, y: H - m.b + 14, class: "axis", "text-anchor": "middle" }, svg);
        lt.textContent = spec.short ? spec.short[i] : spec.labels[i];
      }
      if (spec.type === "bar") {
        var inner = bw * 0.8, off = x0 + bw * 0.1;
        var acc = 0;
        vis.forEach(function (x, j) {
          var v = x.s.values[i];
          if (v === null || v === undefined) return;
          if (spec.stacked) {
            el("rect", { x: off, y: y(acc + v), width: inner, height: Math.max(y(acc) - y(acc + v), 0),
              fill: x.s.color, class: "bar" }, svg);
            acc += v;
          } else {
            var w = inner / vis.length;
            el("rect", { x: off + j * w, y: y(v), width: Math.max(w - 1, 1), height: Math.max(m.t + ph - y(v), 0),
              fill: x.s.color, class: "bar" }, svg);
          }
        });
      } else {
        vis.forEach(function (x) {
          var v = x.s.values[i];
          (linePts[x.i] = linePts[x.i] || []).push(v === null || v === undefined ? null : [x0 + bw / 2, y(v)]);
        });
      }
    }
    if (spec.type === "line") {
      vis.forEach(function (x) {
        var pts = linePts[x.i] || [], d = "", pen = false;
        pts.forEach(function (p) {
          if (!p) { pen = false; return; }
          d += (pen ? "L" : "M") + p[0].toFixed(1) + " " + p[1].toFixed(1);
          pen = true;
        });
        el("path", { d: d, stroke: x.s.color, class: "line" }, svg);
        pts.forEach(function (p) { if (p) el("circle", { cx: p[0], cy: p[1], r: 2.5, fill: x.s.color }, svg); });
      });
    }
    // hover/click targets
    for (var h = 0; h < n; h++) {
      (function (idx) {
        var r = el("rect", { x: m.l + idx * bw, y: m.t, width: bw, height: ph, class: "hit", tabindex: "0" }, svg);
        var link = spec.links ? spec.links[idx] : null;
        if (link) r.classList.add("link");
        var html = function () {
          var st = spec.status ? spec.status[idx] : "complete";
          var s = "<strong>" + esc(spec.labels[idx]) + "</strong>";
          if (st !== "complete") s += '<div class="tip-status ' + esc(st) + '">' + esc(STATUS_TEXT[st] || st) +
            (spec.fraction && st === "partial" ? " (" + Math.round(spec.fraction[idx] * 100) + "%)" : "") + "</div>";
          self.visible().forEach(function (x) {
            s += '<div><span class="swatch" style="background:' + esc(x.s.color) + '"></span>' + esc(x.s.name) +
              ": <b>" + fmt(x.s.values[idx], spec.unit) + "</b></div>";
          });
          if (link) s += '<div class="tip-link">clic per aprire il report</div>';
          return s;
        };
        r.addEventListener("mousemove", function (ev) { showTip(html(), ev.clientX, ev.clientY); });
        r.addEventListener("mouseleave", hideTip);
        r.addEventListener("focus", function () {
          var b = r.getBoundingClientRect();
          showTip(html(), b.left + b.width / 2, b.top);
        });
        r.addEventListener("blur", hideTip);
        if (link) {
          r.addEventListener("click", function () { window.location.href = link; });
          r.addEventListener("keydown", function (ev) { if (ev.key === "Enter") window.location.href = link; });
        }
      })(h);
    }
    el("line", { x1: m.l, x2: W - m.r, y1: m.t + ph, y2: m.t + ph, class: "baseline" }, svg);
  };

  function initCharts() {
    var nodes = document.querySelectorAll("[data-chart]");
    Array.prototype.forEach.call(nodes, function (c) {
      var src = document.getElementById(c.getAttribute("data-chart"));
      if (!src) return;
      try {
        c.chart = new Chart(c, JSON.parse(src.textContent));
      } catch (e) {
        c.textContent = "Grafico non disponibile: " + e.message;
      }
    });
  }

  /* ---------- tables: sorting and filtering ---------- */
  function cellValue(td) {
    var v = td.getAttribute("data-v");
    if (v !== null) return v === "" ? -Infinity : parseFloat(v);
    return td.textContent.trim().toLowerCase();
  }

  function initTables() {
    Array.prototype.forEach.call(document.querySelectorAll("table.sortable"), function (t) {
      Array.prototype.forEach.call(t.querySelectorAll("thead th"), function (th, col) {
        th.tabIndex = 0;
        th.setAttribute("aria-sort", "none");
        var sort = function () {
          var asc = th.getAttribute("aria-sort") !== "ascending";
          Array.prototype.forEach.call(t.querySelectorAll("thead th"), function (o) { o.setAttribute("aria-sort", "none"); });
          th.setAttribute("aria-sort", asc ? "ascending" : "descending");
          var body = t.tBodies[0];
          var rows = Array.prototype.slice.call(body.rows);
          rows.sort(function (a, b) {
            var x = cellValue(a.cells[col]), y = cellValue(b.cells[col]);
            if (x < y) return asc ? -1 : 1;
            if (x > y) return asc ? 1 : -1;
            return 0;
          });
          rows.forEach(function (r) { body.appendChild(r); });
        };
        th.addEventListener("click", sort);
        th.addEventListener("keydown", function (ev) { if (ev.key === "Enter") sort(); });
      });
    });
    Array.prototype.forEach.call(document.querySelectorAll("input[data-filter]"), function (inp) {
      var t = document.getElementById(inp.getAttribute("data-filter"));
      if (!t) return;
      var count = inp.parentNode.querySelector(".filter-count");
      inp.addEventListener("input", function () {
        var q = inp.value.trim().toLowerCase(), shown = 0;
        Array.prototype.forEach.call(t.tBodies[0].rows, function (r) {
          var ok = !q || r.textContent.toLowerCase().indexOf(q) >= 0;
          r.style.display = ok ? "" : "none";
          if (ok) shown++;
        });
        if (count) count.textContent = q ? shown + " righe" : "";
      });
    });
  }

  /* ---------- tabs ---------- */
  // One panel at a time. The selected tab is in the URL fragment (#id) and,
  // for the next page opened, in localStorage: moving to the previous day
  // keeps the same tab. Without JavaScript every panel is shown.
  var TAB_KEY = "postfix-report.tab";

  function store(v) {
    try {
      if (v === undefined) return window.localStorage.getItem(TAB_KEY);
      window.localStorage.setItem(TAB_KEY, v);
    } catch (e) { /* storage unavailable: no memory between pages */ }
    return null;
  }

  function initTabs() {
    var sets = [];
    Array.prototype.forEach.call(document.querySelectorAll(".tabs"), function (box) {
      var list = box.querySelector("[role=tablist]");
      var tabs = Array.prototype.slice.call(list.querySelectorAll("[role=tab]"));
      if (!tabs.length) return;
      var panel = function (t) { return document.getElementById(t.getAttribute("aria-controls")); };
      var select = function (t, focus, remember) {
        tabs.forEach(function (o) {
          var on = o === t;
          o.setAttribute("aria-selected", on ? "true" : "false");
          o.tabIndex = on ? 0 : -1;
          panel(o).hidden = !on;
        });
        Array.prototype.forEach.call(panel(t).querySelectorAll("[data-chart]"), function (c) {
          if (c.chart) c.chart.draw();
        });
        // On narrow screens the tab bar scrolls sideways: keep the selected tab in sight.
        if (t.offsetLeft < list.scrollLeft || t.offsetLeft + t.offsetWidth > list.scrollLeft + list.clientWidth) {
          list.scrollLeft = t.offsetLeft - (list.clientWidth - t.offsetWidth) / 2;
        }
        if (focus) t.focus();
        if (remember) {
          var id = t.getAttribute("aria-controls");
          store(id);
          if (window.history && history.replaceState) history.replaceState(null, "", "#" + id);
        }
      };
      tabs.forEach(function (t, i) {
        t.addEventListener("click", function () { select(t, false, true); });
        t.addEventListener("keydown", function (ev) {
          var j = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[ev.key];
          if (j === undefined) return;
          ev.preventDefault();
          select(tabs[(j + tabs.length) % tabs.length], true, true);
        });
      });
      // The tab holding an element id (a panel or anything inside it).
      var byId = function (id) {
        var e = id && document.getElementById(id);
        while (e && e !== box) {
          if (e.getAttribute("role") === "tabpanel") {
            for (var k = 0; k < tabs.length; k++) if (panel(tabs[k]) === e) return tabs[k];
          }
          e = e.parentNode;
        }
        return null;
      };
      // A fragment selects its tab; the browser has already scrolled to it
      // while every panel was visible, so scroll again to the tab bar.
      var fromHash = function () {
        var t = byId(decodeURIComponent(window.location.hash.slice(1)));
        if (t) {
          select(t, false, false);
          list.scrollIntoView();
        }
        return t;
      };
      list.hidden = false;
      box.classList.add("ready");
      if (fromHash()) {
        // The browser may scroll to the fragment again at load.
        window.addEventListener("load", function () { setTimeout(function () { list.scrollIntoView(); }, 0); });
      } else {
        select(byId(store()) || tabs[0], false, false);
      }
      sets.push(fromHash);
    });
    window.addEventListener("hashchange", function () { sets.forEach(function (f) { f(); }); });
  }

  // The top bar stays fixed: fragments and the tab bar must scroll below it,
  // whatever its height (it wraps on narrow screens).
  function initTopbar() {
    var bar = document.querySelector(".topbar");
    if (!bar) return;
    var fit = function () { document.documentElement.style.scrollPaddingTop = (bar.offsetHeight + 8) + "px"; };
    fit();
    window.addEventListener("resize", fit);
  }

  document.addEventListener("DOMContentLoaded", function () {
    document.documentElement.classList.add("js");
    initTopbar();
    initCharts();
    initTables();
    initTabs();
  });
})();

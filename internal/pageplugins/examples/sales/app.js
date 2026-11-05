// Sales example page. Talks to OpenVibely only through the v1 postMessage bridge.
(function () {
  "use strict";
  var handle = new URLSearchParams(location.search).get("handle") || "";
  var seq = 0;
  var pending = {};
  var report = null;
  var selected = {};
  var loadRevision = 0;

  function send(type, payload) {
    var id = "m" + (++seq);
    window.parent.postMessage({ v: 1, type: type, id: id, handle: handle, payload: payload || {} }, "*");
    return new Promise(function (resolve, reject) { pending[id] = { resolve: resolve, reject: reject }; });
  }

  window.addEventListener("message", function (ev) {
    if (ev.source !== window.parent) return;
    var msg = ev.data;
    if (!msg || msg.v !== 1 || msg.handle !== handle) return;
    if (msg.type === "result" || msg.type === "error") {
      var p = pending[msg.replyTo];
      if (!p) return;
      delete pending[msg.replyTo];
      if (msg.type === "result") p.resolve(msg.payload); else p.reject(new Error((msg.payload && msg.payload.message) || "error"));
    } else if (msg.type === "event") {
      if (msg.payload && (msg.payload.type === "data.changed" || msg.payload.type === "page.refresh")) load("get_report");
    } else if (msg.type === "analysis.delta") {
      var box = document.getElementById("analysis");
      box.style.display = "block";
      box.textContent += msg.payload.text || "";
    } else if (msg.type === "analysis.done") {
      setStatus(msg.payload && msg.payload.error ? "Analysis failed: " + msg.payload.error : "Analysis complete");
      document.getElementById("analyze").disabled = false;
    }
  });

  function fmt(n) { return n.toLocaleString(undefined, { maximumFractionDigits: 0 }); }
  function setStatus(t) { document.getElementById("status").textContent = t; }
  function filters() {
    return {
      region: document.getElementById("region").value,
      from: document.getElementById("from").value,
      to: document.getElementById("to").value
    };
  }
  function visibleRows() {
    if (!report) return [];
    var f = filters();
    return report.rows.filter(function (r) {
      return (!f.from || r.date >= f.from) && (!f.to || r.date <= f.to);
    });
  }

  function render() {
    var tbody = document.getElementById("rows");
    tbody.textContent = "";
    var rows = visibleRows();
    var rev = 0, units = 0;
    rows.forEach(function (r, i) {
      rev += r.revenue; units += r.units;
      var tr = document.createElement("tr");
      var key = r.date + "|" + r.region + "|" + r.product;
      if (selected[key]) tr.className = "selected";
      [r.date, r.region, r.product, fmt(r.units), "$" + fmt(r.revenue)].forEach(function (v, j) {
        var td = document.createElement("td");
        td.textContent = v;
        if (j >= 3) td.className = "num";
        tr.appendChild(td);
      });
      tr.addEventListener("click", function () {
        if (selected[key]) delete selected[key]; else selected[key] = true;
        render(); shareContext().catch(function (e) { setStatus(e.message); });
      });
      tbody.appendChild(tr);
    });
    document.getElementById("revenue").textContent = "$" + fmt(rev);
    document.getElementById("units").textContent = fmt(units);
    document.getElementById("attain").textContent = report && report.target ? Math.round(rev / report.target * 100) + "%" : "–";
    document.getElementById("rev").textContent = report ? String(report.revision) : "–";
  }

  function shareContext() {
    if (!report) return Promise.resolve();
    var f = filters();
    var rows = visibleRows();
    var totals = rows.reduce(function (a, r) { a.revenue += r.revenue; a.units += r.units; return a; }, { revenue: 0, units: 0 });
    var sel = rows.filter(function (r) { return selected[r.date + "|" + r.region + "|" + r.product]; });
    return send("context.set", {
      summary: "Sales report revision " + report.revision + " (" + rows.length + " rows shown, region " + f.region + ")",
      filters: { region: f.region },
      date_range: f.from || f.to ? { from: f.from, to: f.to } : undefined,
      selection: sel.slice(0, 50).map(function (r) { return r.date + "|" + r.region + "|" + r.product; }),
      data: { totals: totals, target: report.target, rows: rows.slice(0, 60), generated_at: report.generated_at }
    });
  }

  function load(op) {
    var revision = ++loadRevision;
    setStatus(op === "refresh_data" ? "Refreshing…" : "Loading…");
    return send("op.invoke", { operation: op, input: { region: filters().region } }).then(function (out) {
      if (revision !== loadRevision) return;
      report = out; render(); shareContext().catch(function (e) { setStatus(e.message); });
      setStatus("Updated " + new Date(out.generated_at).toLocaleTimeString());
    }).catch(function (e) { setStatus("Error: " + e.message); });
  }

  document.getElementById("refresh").addEventListener("click", function () { load("refresh_data"); });
  document.getElementById("region").addEventListener("change", function () { selected = {}; load("get_report"); });
  ["from", "to"].forEach(function (id) {
    document.getElementById(id).addEventListener("change", function () { render(); shareContext().catch(function (e) { setStatus(e.message); }); });
  });
  document.getElementById("analyze").addEventListener("click", function () {
    var box = document.getElementById("analysis");
    box.textContent = ""; box.style.display = "block";
    document.getElementById("analyze").disabled = true;
    setStatus("Analyzing…");
    shareContext().then(function () { return send("analyze.request", { prompt: "Analyze the sales report currently shown (respect the filters and selection). Highlight trends, best/worst regions and products, and target attainment in a few bullets." }); })
      .catch(function (e) { setStatus("Analyze failed: " + e.message); document.getElementById("analyze").disabled = false; });
  });

  send("ready", {}).then(function () { return send("events.subscribe", {}); }).then(function () { load("get_report"); })
    .catch(function (e) { setStatus("Bridge error: " + e.message); });
})();

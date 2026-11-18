// Tab 4: research. Generate constellations with several strategies and compare
// them with a sky culture (Jaccard index of the edges).

import { api, getToken } from "./api.js";
import { $, el, badge, fill, time } from "./dom.js";
import { errorText, strategyText } from "./i18n.js";

const STRATEGIES = ["base", "sectors", "imperial", "star_paths", "cycles"];
const pct = (v) => (v * 100).toFixed(1) + " %";

const numbers = (text) => text.split(/[\s,;]+/).filter(Boolean).map(Number).filter((n) => Number.isFinite(n));

export function initResearch({ onError }) {
  const culture = $("#sweep-culture");
  const strategies = $("#sweep-strategies");
  const list = $("#sweep-list");
  const msg = $("#sweep-msg");
  const runBtn = $("#sweep-run");
  const csvBtn = $("#sweep-csv");
  const summary = $("#sweep-summary tbody");
  const runsBody = $("#sweep-runs tbody");
  let current = null;
  let timer = null;

  for (const s of STRATEGIES) {
    strategies.append(el("label", {}, el("input", { type: "checkbox", value: s, checked: true }), " " + strategyText(s)));
  }

  async function loadCultures() {
    try {
      const { items } = await api("/catalog/cultures");
      const keep = culture.value;
      culture.replaceChildren(...items.map((c) => el("option", { value: c.id }, c.id)));
      culture.value = items.some((c) => c.id === keep) ? keep : (items.find((c) => c.id === "modern_iau") ? "modern_iau" : items[0]?.id);
    } catch (e) {
      culture.replaceChildren();
      if (e.code !== "NO_ACTIVE_CATALOG") onError(e);
    }
  }

  async function loadSweeps(select) {
    try {
      const { items } = await api("/research/sweeps?limit=30");
      list.replaceChildren(...items.map((s) =>
        el("option", { value: s.id }, `${s.id} · ${s.culture} · ${s.status === "running" ? `${s.done}/${s.total}` : s.status === "failed" ? "помилка" : s.total}`)));
      const want = select || current || items[0]?.id;
      if (want && items.some((s) => s.id === want)) list.value = want;
      await show(list.value);
    } catch (e) { onError(e); }
  }

  async function show(id) {
    clearTimeout(timer);
    current = id || null;
    csvBtn.disabled = !current;
    if (!current) {
      fill(summary, [], 5, "Свіпів ще немає: запустіть перший.");
      fill(runsBody, [], 11, "");
      return;
    }
    let data;
    try { data = await api("/research/sweeps/" + encodeURIComponent(current)); } catch (e) { onError(e); return; }
    const { sweep, runs } = data;
    if (sweep.status === "running") {
      msg.textContent = `Виконується: ${sweep.done} з ${sweep.total}…`;
      timer = setTimeout(() => loadSweeps(sweep.id), 1000);
    } else if (sweep.status === "failed") {
      msg.replaceChildren(badge("fail", "помилка"), " " + errorText(sweep.error_code), sweep.error_detail ? ` (${sweep.error_detail})` : "");
    } else {
      msg.replaceChildren(badge("ok", "готово"), ` ${sweep.total} запусків, збірка ${sweep.build_id}, ${time(sweep.finished_at)}`);
    }
    renderSummary(runs);
    renderRuns(runs);
  }

  function renderSummary(runs) {
    const by = new Map();
    for (const r of runs) {
      const g = by.get(r.params.strategy) || { n: 0, sum: 0, best: null };
      g.n++; g.sum += r.metrics.jaccard;
      if (!g.best || r.metrics.jaccard > g.best.metrics.jaccard) g.best = r;
      by.set(r.params.strategy, g);
    }
    const rows = [...by.entries()].sort((a, b) => b[1].best.metrics.jaccard - a[1].best.metrics.jaccard);
    fill(summary, rows.map(([s, g], i) => el("tr", { class: i === 0 ? "best" : "" },
      el("td", {}, strategyText(s)),
      el("td", { class: "num" }, g.n),
      el("td", { class: "num", "data-sort": g.best.metrics.jaccard }, pct(g.best.metrics.jaccard)),
      el("td", { class: "num", "data-sort": g.sum / g.n }, pct(g.sum / g.n)),
      el("td", {}, `відстань ${g.best.params.eps_deg}°, seed ${g.best.params.seed}`),
    )), 5, "Немає результатів.");
  }

  function renderRuns(runs) {
    fill(runsBody, runs.map((r) => el("tr", {},
      el("td", {}, strategyText(r.params.strategy)),
      el("td", { class: "num" }, r.params.eps_deg),
      el("td", { class: "num" }, r.params.seed),
      el("td", { class: "num" }, r.stars),
      el("td", { class: "num" }, r.edges),
      el("td", { class: "num" }, r.clusters),
      el("td", { "data-sort": r.metrics.jaccard }, el("span", { class: "bar" }, el("span", { style: `width:${Math.round(r.metrics.jaccard * 100)}%` })), pct(r.metrics.jaccard)),
      el("td", { class: "num", "data-sort": r.metrics.precision }, pct(r.metrics.precision)),
      el("td", { class: "num", "data-sort": r.metrics.recall }, pct(r.metrics.recall)),
      el("td", {}, el("code", { title: r.hash }, r.hash.slice(0, 10) + "…")),
      el("td", { class: "num" }, r.duration_ms.toFixed(0)),
    )), 11, "Немає запусків.");
  }

  function spec() {
    return {
      culture: culture.value,
      strategies: [...strategies.querySelectorAll("input:checked")].map((i) => i.value),
      eps_list: numbers($("#sweep-eps").value),
      seeds: numbers($("#sweep-seeds").value),
      params: { mag_max: Number($("#sweep-mag").value) || 0, jitter_deg: Number($("#sweep-jitter").value) || 0 },
    };
  }

  $("#sweep-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    runBtn.disabled = true;
    msg.textContent = "";
    try {
      const sw = await api("/research/sweeps", { method: "POST", body: spec() });
      await loadSweeps(sw.id);
    } catch (err) {
      msg.textContent = err.params?.reason || errorText(err);
      onError(err);
    }
    runBtn.disabled = false;
  });

  $("#sweep-verify").addEventListener("click", async () => {
    msg.textContent = "Перевірка…";
    try {
      const body = { ...spec() };
      body.strategies = body.strategies.slice(0, 1);
      const v = await api("/research/verify", { method: "POST", body });
      msg.replaceChildren(badge(v.equal ? "ok" : "fail", v.equal ? "детерміновано" : "РІЗНІ ХЕШІ"),
        ` ${strategyText(v.params.strategy)}, seed ${v.params.seed}: `, el("code", {}, v.hash1.slice(0, 12)), " і ", el("code", {}, v.hash2.slice(0, 12)), `, ребер ${v.edges}`);
    } catch (err) { msg.textContent = err.params?.reason || errorText(err); onError(err); }
  });

  // The CSV needs the token, so it is fetched and saved as a file.
  csvBtn.addEventListener("click", async () => {
    if (!current) return;
    try {
      const base = location.port && location.port !== "8080" ? "http://localhost:8080" : "";
      const resp = await fetch(`${base}/api/research/sweeps/${encodeURIComponent(current)}/csv`, { headers: { Authorization: "Bearer " + getToken() } });
      if (!resp.ok) throw new Error(resp.status);
      const url = URL.createObjectURL(await resp.blob());
      const a = el("a", { href: url, download: current + ".csv" });
      document.body.append(a); a.click(); a.remove(); URL.revokeObjectURL(url);
    } catch { msg.textContent = "Не вдалося завантажити CSV."; }
  });

  list.addEventListener("change", () => show(list.value));

  return { refresh: async () => { await loadCultures(); await loadSweeps(); } };
}

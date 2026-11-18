// Tab 3: releases. Publish a validated build in classic mode, see the saga
// result, history, and the published version.json.

import { api } from "./api.js";
import { $, el, badge, fill, time, int } from "./dom.js";
import { statusText, errorText, stepText } from "./i18n.js";

const COLUMNS = 7;

export function initReleases({ onError }) {
  const buildSel = $("#release-build");
  const classicBtn = $("#publish-classic");
  const edaBtn = $("#publish-eda");
  const result = $("#release-result");
  const tbody = $("#releases-table tbody");
  const manifest = $("#manifest");

  async function loadBuilds() {
    let reports = [];
    try { reports = (await api("/catalog/reports?limit=50")).items; } catch (e) { onError(e); }
    const ok = reports.filter((r) => r.verdict === "validated");
    const keep = buildSel.value;
    buildSel.replaceChildren(...ok.map((r) => el("option", { value: r.build_id }, r.build_id)));
    if (keep && ok.some((r) => r.build_id === keep)) buildSel.value = keep;
    classicBtn.disabled = edaBtn.disabled = !ok.length;
  }

  let timer = null;

  async function loadHistory() {
    clearTimeout(timer);
    try {
      const { items } = await api("/release/releases?limit=30");
      fill(tbody, items.map(row), COLUMNS, "Релізів ще немає.");
      // Intermediate steps send no event, so poll while a release is in progress.
      if (items.some((r) => r.status !== "published" && r.status !== "failed")) timer = setTimeout(loadHistory, 1500);
    } catch (e) {
      fill(tbody, [], COLUMNS, errorText(e));
      onError(e);
    }
  }

  const open = new Set(); // failed releases whose reason is shown

  function detail(r) {
    const f = r.failure;
    const box = el("div", { class: "reason" },
      el("b", {}, `Крок «${stepText(f.step)}»: ${errorText(f.code)}`), " ", el("code", {}, f.code),
      el("div", { class: "muted small" }, f.compensated ? "Відкат виконано: version.json відновлено, резерв знято." : "Відкат НЕ завершено, потрібне ручне втручання."),
      f.detail ? el("pre", {}, f.detail) : null);
    const tl = el("div", { class: "muted small" }, "…");
    box.append(tl);
    // The step history comes from the event stream of the release.
    api(`/release/releases/${encodeURIComponent(r.id)}`).then((full) => {
      tl.replaceChildren("Кроки: ", ...(full.events || []).flatMap((e) =>
        [el("code", {}, e.type), " ", time(e.at).split(", ")[1] || "", " → "]).slice(0, -1));
    }).catch(() => { tl.textContent = ""; });
    return el("tr", { class: "detail", hidden: !open.has(r.id) }, el("td", { colspan: COLUMNS }, box));
  }

  function statusCell(r) {
    const kind = r.status === "published" ? "ok" : r.status === "failed" ? "fail" : "running";
    if (!r.failure) return badge(kind, releaseStatus(r.status));
    const btn = el("button", {
      class: "badge s-failed as-button", title: r.failure.detail || errorText(r.failure.code),
      "aria-expanded": String(open.has(r.id)),
      onclick: () => {
        const d = btn.closest("tr").detailRow;
        d.hidden = !d.hidden;
        d.hidden ? open.delete(r.id) : open.add(r.id);
        btn.setAttribute("aria-expanded", String(!d.hidden));
      },
    }, releaseStatus(r.status) + " ▾");
    return btn;
  }

  const row = (r) => withDetail(el("tr", {},
    el("td", {}, el("code", {}, r.id), r.trace_id ? [" ", el("a", { href: "http://localhost:16686/trace/" + r.trace_id, target: "_blank", rel: "noopener", title: "Трейс у Jaeger" }, "↗")] : null),
    el("td", {}, el("code", {}, r.build_id)),
    el("td", {}, r.mode === "classic" ? "класичний" : "події"),
    el("td", { "data-sort": r.status }, statusCell(r)),
    el("td", { "data-sort": r.started_at }, time(r.started_at)),
    el("td", { class: "num", "data-sort": r.duration_ms ?? "" }, r.duration_ms != null ? `${int(r.duration_ms)} мс` : "…"),
    el("td", {}, r.version ? el("code", {}, r.version) : "—"),
  ), r);

  function withDetail(tr, r) {
    if (r.failure) tr.detailRow = detail(r);
    return tr;
  }

  const releaseStatus = (s) => ({ published: "опубліковано", failed: "помилка, відкат", requested: "запит", reserved: "зарезервовано", switched: "перемкнуто" }[s] || statusText(s));

  async function loadManifest() {
    try {
      manifest.textContent = JSON.stringify(await api("/release/version"), null, 2);
    } catch (e) {
      manifest.textContent = e.code === "NOT_FOUND" ? "Ще нічого не опубліковано." : errorText(e);
      if (e.code !== "NOT_FOUND") onError(e);
    }
  }

  classicBtn.addEventListener("click", async () => {
    classicBtn.disabled = true;
    result.replaceChildren(el("span", { class: "muted" }, "Публікація…"));
    const t0 = performance.now();
    try {
      const r = await api("/release/releases?mode=classic", { method: "POST", body: { build_id: buildSel.value } });
      result.replaceChildren(badge("ok", "опубліковано"), ` ${r.id} за ${Math.round(performance.now() - t0)} мс (відповідь після всіх кроків)`);
    } catch (e) {
      const p = e.params || {};
      const text = e.code === "RELEASE_FAILED"
        ? ` Крок «${stepText(p.step)}»: ${errorText(p.reason)}${p.compensated ? " Відкат виконано, реліз скасовано." : " Відкат не завершено!"}`
        : " " + errorText(e);
      result.replaceChildren(badge("fail", "помилка"), text, ` (${Math.round(performance.now() - t0)} мс)`);
      onError(e);
    }
    await refresh();
  });

  edaBtn.addEventListener("click", async () => {
    edaBtn.disabled = true;
    result.replaceChildren(el("span", { class: "muted" }, "Запит…"));
    const t0 = performance.now();
    try {
      const r = await api("/release/releases?mode=eda", { method: "POST", body: { build_id: buildSel.value } });
      result.replaceChildren(badge("running", "прийнято"),
        ` ${r.id}: відповідь за ${Math.round(performance.now() - t0)} мс (202), далі працює воркер, статус оновлюється з потоку подій.`);
    } catch (e) {
      result.replaceChildren(badge("fail", "помилка"), " " + errorText(e));
      onError(e);
    }
    edaBtn.disabled = !buildSel.options.length;
    await refresh();
  });

  async function refresh() {
    await Promise.all([loadBuilds(), loadHistory(), loadManifest()]);
  }

  return { refresh };
}

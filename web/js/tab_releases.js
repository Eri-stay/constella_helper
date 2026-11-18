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
    classicBtn.disabled = !ok.length;
  }

  async function loadHistory() {
    try {
      const { items } = await api("/release/releases?limit=30");
      fill(tbody, items.map(row), COLUMNS, "Релізів ще немає.");
    } catch (e) {
      fill(tbody, [], COLUMNS, errorText(e));
      onError(e);
    }
  }

  const row = (r) => el("tr", {},
    el("td", {}, el("code", {}, r.id)),
    el("td", {}, el("code", {}, r.build_id)),
    el("td", {}, r.mode === "classic" ? "класичний" : "події"),
    el("td", {}, badge(r.status === "published" ? "ok" : r.status === "failed" ? "fail" : "running", releaseStatus(r.status)),
      r.failure ? el("span", { class: "muted small" }, ` ${stepText(r.failure.step)}: ${errorText(r.failure.code)}${r.failure.compensated ? " (відкат виконано)" : ""}`) : null),
    el("td", {}, time(r.started_at)),
    el("td", { class: "num" }, r.duration_ms != null ? `${int(r.duration_ms)} мс` : "…"),
    el("td", {}, r.version ? el("code", {}, r.version) : "—"),
  );

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

  edaBtn.addEventListener("click", () => {
    result.textContent = "Режим подій з'явиться на етапі M3.";
  });

  async function refresh() {
    await Promise.all([loadBuilds(), loadHistory(), loadManifest()]);
  }

  return { refresh };
}

// Tab 2: validation report, star search, cultures and figures.

import { api } from "./api.js";
import { $, el, badge, fill, time, int } from "./dom.js";
import { statusText, errorText, checkTitle, checkDetail } from "./i18n.js";
import { drawFigure } from "./figure.js";

export function initVerify({ onError }) {
  const buildSel = $("#report-build");
  const verdictEl = $("#report-verdict");
  const reportBody = $("#report-table tbody");
  const starForm = $("#star-form");
  const starQ = $("#star-q");
  const starMsg = $("#star-msg");
  const starsBody = $("#stars-table tbody");
  const cultureSel = $("#culture");
  const cultureInfo = $("#culture-info");
  const figuresBody = $("#figures-table tbody");
  const svg = $("#figure-svg");
  const caption = $("#figure-caption");

  // Everything below the report belongs to the build chosen in the list.
  const build = () => (buildSel.value ? "&build=" + encodeURIComponent(buildSel.value) : "");

  let reports = [];
  let cultures = [];

  // ---- report ----------------------------------------------------------

  async function loadReports(select) {
    try {
      reports = (await api("/catalog/reports?limit=50")).items;
    } catch (e) {
      onError(e);
      reports = [];
    }
    const keep = select || buildSel.value;
    buildSel.replaceChildren(...reports.map((r) =>
      el("option", { value: r.build_id }, `${r.build_id} (${statusText(r.verdict)})`)));
    if (keep && reports.some((r) => r.build_id === keep)) buildSel.value = keep;
    showReport();
  }

  function showReport() {
    const r = reports.find((x) => x.build_id === buildSel.value);
    verdictEl.replaceChildren();
    if (!r) {
      fill(reportBody, [], 3, "Звітів ще немає: спершу запустіть збірку на вкладці «Збірки».");
      return;
    }
    verdictEl.append(badge(r.verdict, statusText(r.verdict)), " ", el("span", { class: "muted" }, time(r.checked_at)));
    fill(reportBody, r.checks.map((c) => el("tr", {},
      el("td", {}, checkTitle(c)),
      el("td", { "data-sort": c.status }, badge(c.status, statusText(c.status))),
      el("td", { class: "muted" }, checkDetail(c)),
    )), 3, "");
  }

  buildSel.addEventListener("change", () => { showReport(); searchStars(); loadCultures(); });

  // ---- star search -----------------------------------------------------

  async function searchStars() {
    starMsg.textContent = "";
    try {
      const { items } = await api("/catalog/stars?limit=25&q=" + encodeURIComponent(starQ.value) + build());
      fill(starsBody, items.map(starRow), 9, "Нічого не знайдено.");
      starMsg.textContent = items.length ? `Знайдено: ${items.length}` : "";
    } catch (e) {
      fill(starsBody, [], 9, errorText(e));
      if (e.code !== "NO_ACTIVE_CATALOG") onError(e);
    }
  }

  const starRow = (s) => el("tr", {},
    el("td", {}, s.name || "—"),
    el("td", {}, s.name_uk || "—"),
    el("td", { class: "num" }, s.hip || "—"),
    el("td", {}, s.bayer || "—"),
    el("td", {}, s.con || "—"),
    el("td", { class: "num" }, s.mag.toFixed(2)),
    el("td", { class: "num" }, s.ra.toFixed(4)),
    el("td", { class: "num" }, s.dec.toFixed(3)),
    el("td", {}, s.spect || "—"),
  );

  starForm.addEventListener("submit", (e) => { e.preventDefault(); searchStars(); });

  // ---- cultures --------------------------------------------------------

  async function loadCultures() {
    try {
      cultures = (await api("/catalog/cultures?" + build().slice(1))).items;
    } catch (e) {
      cultures = [];
      if (e.code !== "NO_ACTIVE_CATALOG") onError(e);
    }
    const keep = cultureSel.value;
    cultureSel.replaceChildren(...cultures.map((c) => el("option", { value: c.id }, c.id)));
    if (keep && cultures.some((c) => c.id === keep)) cultureSel.value = keep;
    await showCulture();
  }

  async function showCulture() {
    const c = cultures.find((x) => x.id === cultureSel.value);
    svg.replaceChildren();
    caption.textContent = "Оберіть фігуру зі списку.";
    if (!c) {
      cultureInfo.textContent = "";
      fill(figuresBody, [], 3, "Немає перевіреного каталогу.");
      return;
    }
    cultureInfo.textContent = `${c.region} · ${c.classification} · фігур ${c.constellations}, ребер ${int(c.edges)}`;
    try {
      const { items } = await api("/catalog/cultures/" + encodeURIComponent(c.id) + "?" + build().slice(1));
      fill(figuresBody, items.map((f) => {
        const tr = el("tr", { class: "clickable", tabindex: "0" },
          el("td", {}, el("code", {}, f.id)),
          el("td", {}, [f.name_en, f.name_native].filter(Boolean).join(" · ") || "—"),
          el("td", { class: "num" }, f.edges));
        const open = () => showFigure(c.id, f, tr);
        tr.addEventListener("click", open);
        tr.addEventListener("keydown", (e) => { if (e.key === "Enter") open(); });
        return tr;
      }), 3, "У культури немає фігур.");
    } catch (e) {
      onError(e);
    }
  }

  async function showFigure(culture, f, tr) {
    figuresBody.querySelectorAll("tr.selected").forEach((r) => r.classList.remove("selected"));
    tr.classList.add("selected");
    try {
      const fig = await api(`/catalog/cultures/${encodeURIComponent(culture)}/figures/${encodeURIComponent(f.id)}?${build().slice(1)}`);
      const drawn = drawFigure(svg, fig);
      caption.textContent = drawn
        ? `${[fig.name_en, fig.name_native].filter(Boolean).join(" · ") || fig.id}: зір ${fig.stars.length}, ребер ${fig.edges}`
        : "У цієї фігури немає зір із каталогу HIP.";
    } catch (e) {
      onError(e);
    }
  }

  cultureSel.addEventListener("change", showCulture);

  return {
    /** Loads everything; selectBuild chooses the report to show. */
    async refresh(selectBuild) {
      await loadReports(selectBuild); // first: it decides which build the rest reads
      await Promise.all([searchStars(), loadCultures()]);
    },
  };
}

// Tab 1: builds. Start the ETL, watch it run, see the catalog's verdict.

import { api, ApiError } from "./api.js";
import { $, el, badge, fill, time, int, duration, bytes } from "./dom.js";
import { statusText, errorText } from "./i18n.js";

const COLUMNS = 12;

export function initBuilds({ onError, openReport }) {
  const tbody = $("#builds-table tbody");
  const startBtn = $("#start-build");
  const msg = $("#build-msg");
  let timer = null;

  async function refresh() {
    clearTimeout(timer);
    let builds, reports;
    try {
      builds = (await api("/dataprep/builds?limit=30")).items;
    } catch (e) {
      onError(e);
      fill(tbody, [], COLUMNS, errorText(e));
      return;
    }
    // The verdict belongs to catalog; if it is down the builds still show.
    try {
      reports = new Map((await api("/catalog/reports?limit=100")).items.map((r) => [r.build_id, r]));
    } catch {
      reports = new Map();
    }
    fill(tbody, builds.map((b) => row(b, reports.get(b.id))), COLUMNS, "Збірок ще немає. Натисніть «Запустити збірку».");
    startBtn.disabled = builds.some((b) => b.status === "running");
    if (builds.some((b) => b.status === "running" || (b.status === "completed" && !reports.has(b.id)))) {
      timer = setTimeout(refresh, 2000); // fallback if the event stream is down
    }
  }

  const open = new Set(); // ids of failed builds whose reason is shown

  function detail(b) {
    return el("tr", { class: "detail", hidden: !open.has(b.id) },
      el("td", { colspan: COLUMNS },
        el("div", { class: "reason" },
          el("b", {}, errorText(b.error_code)), " ", el("code", {}, b.error_code),
          b.error_detail ? el("pre", {}, b.error_detail) : null)));
  }

  function row(b, report) {
    const verdict = report
      ? badge(report.verdict, statusText(report.verdict))
      : b.status === "completed" ? badge("running", "перевіряється…") : badge("none", "—");
    const edges = b.dropped_edges ? `${int(b.edges)} (−${b.dropped_edges})` : int(b.edges);
    const sha = b.sha256
      ? el("code", { title: b.sha256 }, b.sha256.slice(0, 12) + "…")
      : "—";
    let status = badge(b.status, statusText(b.status));
    let detailRow = null;
    if (b.error_code) {
      detailRow = detail(b);
      status = el("button", {
        class: "badge s-failed as-button", title: b.error_detail || errorText(b.error_code),
        "aria-expanded": String(open.has(b.id)),
        onclick: (e) => {
          const show = detailRow.hidden;
          detailRow.hidden = !show;
          show ? open.add(b.id) : open.delete(b.id);
          e.currentTarget.setAttribute("aria-expanded", String(show));
        },
      }, statusText(b.status) + " ▾");
    }
    const tr = el("tr", {},
      el("td", {}, el("code", {}, b.id)),
      el("td", { "data-sort": b.status }, status),
      el("td", { "data-sort": b.started_at }, time(b.started_at)),
      el("td", { "data-sort": b.finished_at ? new Date(b.finished_at) - new Date(b.started_at) : "" },
        b.status === "running" ? "…" : duration(b.started_at, b.finished_at)),
      el("td", { class: "num" }, int(b.stars)),
      el("td", { class: "num" }, int(b.constellations)),
      el("td", { class: "num" }, int(b.cultures)),
      el("td", { class: "num", title: b.dropped_edges ? "у дужках: ребра, відкинуті через відсутню HIP-зорю" : "" }, edges),
      el("td", { class: "num" }, bytes(b.size_bytes)),
      el("td", {}, sha),
      el("td", {}, verdict),
      el("td", {}, report ? el("button", { class: "link", onclick: () => openReport(b.id) }, "Звіт") : null),
    );
    tr.detailRow = detailRow;
    return tr;
  }

  startBtn.addEventListener("click", async () => {
    startBtn.disabled = true;
    msg.textContent = "";
    try {
      const b = await api("/dataprep/builds", { method: "POST" });
      msg.textContent = `Збірку ${b.id} запущено.`;
    } catch (e) {
      msg.textContent = e instanceof ApiError && e.params?.reason ? errorText(e.params.reason) : errorText(e);
      onError(e);
    }
    refresh();
  });

  return { refresh };
}

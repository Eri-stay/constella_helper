// Tab 6: fault injection. Latency or failures per service through
// /debug/fault, so the Task 2 experiments can be driven without restarting containers.

import { api } from "./api.js";
import { $, el, badge, fill } from "./dom.js";
import { errorText } from "./i18n.js";

const SERVICES = ["dataprep", "catalog", "release", "telemetry"];

export function initFaults({ onError }) {
  const tbody = $("#faults-table tbody");
  const breakers = $("#breakers");
  const msg = $("#faults-msg");

  const state = new Map(); // service -> current settings or null when unreachable

  async function load() {
    await Promise.all(SERVICES.map(async (s) => {
      try { state.set(s, await api(`/${s}/debug/fault`)); } catch (e) { state.set(s, null); }
    }));
    render();
    try {
      const { breakers: b } = await api("/overview");
      const entries = Object.entries(b || {});
      breakers.replaceChildren(...(entries.length
        ? entries.flatMap(([host, st]) => [el("code", {}, host), " ", badge(st === "closed" ? "ok" : st === "open" ? "fail" : "warn", st), "  "])
        : [el("span", { class: "muted" }, "ще немає викликів")]));
    } catch (e) { onError(e); }
  }

  function render() {
    fill(tbody, SERVICES.map(row), 6, "");
  }

  function row(s) {
    const cur = state.get(s);
    const lat = el("input", { type: "number", min: "0", max: "60000", step: "100", value: cur?.latency_ms ?? 0, "aria-label": `Затримка ${s}`, size: "6" });
    const fail = el("input", { type: "number", min: "0", max: "100", step: "10", value: Math.round((cur?.fail_rate ?? 0) * 100), "aria-label": `Частка помилок ${s}`, size: "4" });
    const apply = async (latency, rate) => {
      msg.textContent = "";
      try {
        await api(`/${s}/debug/fault`, { method: "POST", body: { latency_ms: latency, fail_rate: rate, status: 503 } });
        msg.textContent = `${s}: збережено.`;
      } catch (e) { msg.textContent = `${s}: ${errorText(e)}`; onError(e); }
      load();
    };
    const active = cur && (cur.latency_ms > 0 || cur.fail_rate > 0);
    return el("tr", {},
      el("td", {}, el("code", {}, s)),
      el("td", {}, cur === null ? badge("fail", "недоступний") : active ? badge("warn", "збій увімкнено") : badge("ok", "норма")),
      el("td", {}, lat, " мс"),
      el("td", {}, fail, " %"),
      el("td", {},
        el("button", { onclick: () => apply(Number(lat.value) || 0, (Number(fail.value) || 0) / 100) }, "Застосувати"), " ",
        el("button", { class: "link", onclick: () => apply(0, 0) }, "Скинути")),
      el("td", {}, s === "catalog" ? el("button", { class: "link", onclick: () => apply(2000, 0) }, "+2 с") : null),
    );
  }

  return { refresh: load };
}

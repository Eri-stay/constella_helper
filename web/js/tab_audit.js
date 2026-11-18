// Tab 5: audit trail and client logs. Failures are marked, and every row can
// show its data or stack trace.

import { api } from "./api.js";
import { $, el, badge, fill, time } from "./dom.js";
import { errorText } from "./i18n.js";

// What an event type says about the outcome of a step.
function outcome(type) {
  if (/\.(failed|rejected)$/.test(type)) return ["fail", "помилка"];
  if (/\.(published|completed|validated)$/.test(type)) return ["ok", "успіх"];
  return ["none", "—"];
}

/** A button that shows or hides a full-width row under its table row. */
function toggle(row, label, content) {
  const detail = el("tr", { class: "detail", hidden: true }, el("td", { colspan: row.children.length + 1 }, content));
  const btn = el("button", {
    class: "link", "aria-expanded": "false",
    onclick: () => {
      detail.hidden = !detail.hidden;
      btn.setAttribute("aria-expanded", String(!detail.hidden));
    },
  }, label + " ▾");
  row.append(el("td", {}, btn));
  row.detailRow = detail;
  return row;
}

export function initAudit({ onError }) {
  const typeInput = $("#audit-type");
  const onlyErrors = $("#only-errors");
  const auditBody = $("#audit-table tbody");
  const logsBody = $("#logs-table tbody");
  const sinkEl = $("#sink-info");
  const logForm = $("#log-form");
  const logMsg = $("#log-msg");
  const logResult = $("#log-result");

  async function loadAudit() {
    try {
      const { items } = await api("/telemetry/audit?limit=200&type=" + encodeURIComponent(typeInput.value.trim()));
      const shown = items.filter((a) => !onlyErrors.checked || outcome(a.type)[0] === "fail");
      fill(auditBody, shown.map((a) => {
        const [kind, label] = outcome(a.type);
        const row = el("tr", {},
          el("td", { class: "num" }, a.id),
          el("td", { "data-sort": a.at }, time(a.at)),
          el("td", {}, a.source),
          el("td", {}, el("code", {}, a.type)),
          el("td", { "data-sort": kind }, badge(kind, label)),
          el("td", {}, a.correlation_id ? el("code", {}, a.correlation_id) : "—"),
          el("td", {}, badge(a.via === "sync" ? "running" : "none", a.via === "sync" ? "синхронно" : "подія")),
        );
        return a.data ? toggle(row, "Дані", el("pre", { class: "code" }, JSON.stringify(a.data, null, 2))) : row.appendChild(el("td")) && row;
      }), 8, onlyErrors.checked ? "Помилок немає." : "Записів немає.");
    } catch (e) {
      fill(auditBody, [], 8, errorText(e));
      onError(e);
    }
  }

  async function loadLogs() {
    try {
      const { items } = await api("/telemetry/client-log?limit=200");
      const shown = items.filter((l) => !onlyErrors.checked || l.level === "error" || l.level === "fatal");
      fill(logsBody, shown.map((l) => {
        const kind = l.level === "info" ? "none" : l.level === "warn" ? "warn" : "fail";
        const row = el("tr", {},
          el("td", { class: "num" }, l.id),
          el("td", { "data-sort": l.at }, time(l.at)),
          el("td", { "data-sort": l.level }, badge(kind, l.level)),
          el("td", { class: "wrap" }, l.message),
          el("td", {}, l.installation || "—"),
          el("td", {}, l.app_version || "—"),
        );
        return l.stack ? toggle(row, "Стек", el("pre", { class: "code" }, l.stack)) : row.appendChild(el("td")) && row;
      }), 7, onlyErrors.checked ? "Помилок немає." : "Логів клієнтів немає.");
    } catch (e) {
      fill(logsBody, [], 7, errorText(e));
      onError(e);
    }
  }

  async function loadSink() {
    try {
      const s = await api("/telemetry/summary");
      sinkEl.textContent = `Sentry: ${s.sink === "sentry" ? "увімкнено" : "вимкнено (лише локальний аудит)"}`;
    } catch { sinkEl.textContent = ""; }
  }

  typeInput.addEventListener("input", loadAudit);
  onlyErrors.addEventListener("change", () => { loadAudit(); loadLogs(); });

  // A demo of the scrubber: the user name must never be stored.
  logForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      const l = await api("/telemetry/client-log", {
        method: "POST",
        body: {
          level: "error", installation: "demo-pc", app_version: "0.1.0-demo", message: logMsg.value,
          stack: "at Open (C:\\Users\\Марія\\Documents\\Constella\\app.gd:42)",
        },
      });
      logResult.textContent = "Збережено як: " + l.message;
      await loadLogs();
    } catch (err) {
      logResult.textContent = errorText(err);
      onError(err);
    }
  });

  return { refresh: () => Promise.all([loadAudit(), loadLogs(), loadSink()]) };
}

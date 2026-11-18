// Tab 5: audit trail and client logs.

import { api } from "./api.js";
import { $, el, badge, fill, time } from "./dom.js";
import { errorText } from "./i18n.js";

export function initAudit({ onError }) {
  const typeInput = $("#audit-type");
  const auditBody = $("#audit-table tbody");
  const logsBody = $("#logs-table tbody");
  const sinkEl = $("#sink-info");
  const logForm = $("#log-form");
  const logMsg = $("#log-msg");
  const logResult = $("#log-result");

  async function loadAudit() {
    try {
      const { items } = await api("/telemetry/audit?limit=100&type=" + encodeURIComponent(typeInput.value.trim()));
      fill(auditBody, items.map((a) => el("tr", {},
        el("td", { "data-sort": a.at }, time(a.at)),
        el("td", {}, a.source),
        el("td", {}, el("code", {}, a.type)),
        el("td", {}, a.correlation_id ? el("code", {}, a.correlation_id) : "—"),
        el("td", {}, badge(a.via === "sync" ? "running" : "none", a.via === "sync" ? "синхронно" : "подія")),
      )), 5, "Записів немає.");
    } catch (e) {
      fill(auditBody, [], 5, errorText(e));
      onError(e);
    }
  }

  async function loadLogs() {
    try {
      const { items } = await api("/telemetry/client-log?limit=50");
      fill(logsBody, items.map((l) => el("tr", {},
        el("td", { "data-sort": l.at }, time(l.at)),
        el("td", {}, badge(l.level === "info" ? "none" : l.level === "warn" ? "warn" : "fail", l.level)),
        el("td", { class: "wrap" }, l.message),
        el("td", {}, l.installation || "—"),
      )), 4, "Логів клієнтів немає.");
    } catch (e) {
      fill(logsBody, [], 4, errorText(e));
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

  // A demo of the scrubber: the user name must never be stored.
  logForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      const l = await api("/telemetry/client-log", {
        method: "POST",
        body: { level: "error", installation: "demo-pc", message: logMsg.value },
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

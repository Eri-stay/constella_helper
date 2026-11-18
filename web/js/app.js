// Admin UI shell: tabs, token, live event stream, service overview.

import { api, getToken, setToken, openStream, ApiError } from "./api.js";
import { $, el, badge } from "./dom.js";
import { errorText } from "./i18n.js";
import { initBuilds } from "./tab_builds.js";
import { initVerify } from "./tab_verify.js";
import { initReleases } from "./tab_releases.js";
import { initAudit } from "./tab_audit.js";

const banner = $("#banner");
const tokenInput = $("#token");

function onError(e) {
  if (e instanceof ApiError && e.status === 401) {
    banner.textContent = errorText("UNAUTHORIZED") + " Введіть токен угорі справа.";
    banner.hidden = false;
  } else if (e instanceof ApiError && e.status === 0) {
    banner.textContent = errorText("UNREACHABLE");
    banner.hidden = false;
  }
}

const clearBanner = () => { banner.hidden = true; };

// ---- tabs -----------------------------------------------------------------

const tabs = [...document.querySelectorAll("#tabs [role=tab]")];
const panels = new Map(tabs.map((t) => [t.dataset.tab, $("#tab-" + t.dataset.tab)]));
let verify;
let builds;
let releases;
let audit;

function select(name) {
  for (const t of tabs) t.setAttribute("aria-selected", String(t.dataset.tab === name));
  for (const [n, p] of panels) p.hidden = n !== name;
  try { history.replaceState(null, "", "#" + name); } catch { /* sandboxed frame */ }
  if (name === "builds") builds.refresh();
  if (name === "verify") verify.refresh();
  if (name === "releases") releases.refresh();
  if (name === "audit") audit.refresh();
}

for (const t of tabs) t.addEventListener("click", () => select(t.dataset.tab));

builds = initBuilds({
  onError,
  openReport: async (id) => {
    select("verify");
    await verify.refresh(id);
  },
});
verify = initVerify({ onError });
releases = initReleases({ onError });
audit = initAudit({ onError });

// ---- token ----------------------------------------------------------------

tokenInput.value = getToken();
$("#login").addEventListener("submit", (e) => {
  e.preventDefault();
  setToken(tokenInput.value.trim());
  clearBanner();
  start();
});

// ---- live stream and overview ---------------------------------------------

const streamPill = $("#stream-state");
let closeStream = null;
let debounce = null;

function startStream() {
  closeStream?.();
  closeStream = openStream({
    onState: (up) => {
      streamPill.className = "pill " + (up ? "on" : "off");
      streamPill.textContent = "потік: " + (up ? "підключено" : "вимкнено");
    },
    onEvent: () => {
      clearTimeout(debounce);
      debounce = setTimeout(() => {
        refreshActive();
        refreshOverview();
      }, 150);
    },
  });
}

function refreshActive() {
  const current = tabs.find((t) => t.getAttribute("aria-selected") === "true")?.dataset.tab;
  if (current === "builds") builds.refresh();
  if (current === "verify") verify.refresh();
  if (current === "releases") releases.refresh();
  if (current === "audit") audit.refresh();
}

const overviewEl = $("#overview");

async function refreshOverview() {
  try {
    const { services } = await api("/overview");
    clearBanner();
    overviewEl.replaceChildren(...services.flatMap((s) => [
      el("span", {}, s.name + " "),
      badge(s.status === "ok" ? "ok" : "fail", s.status === "ok" ? "ок" : errorText(s.error)),
      " ",
    ]));
  } catch (e) {
    overviewEl.textContent = "";
    onError(e);
  }
}

function start() {
  startStream();
  refreshOverview();
  select(location.hash.slice(1) in Object.fromEntries(panels) ? location.hash.slice(1) : "builds");
}

setInterval(refreshOverview, 10000);
start();

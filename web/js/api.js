// API client: every call goes through the gateway with the bearer token.

// Served by the gateway: same origin. Served by another static server (Live Server on :5500): call the gateway.
const BASE = location.port && location.port !== "8080" ? "http://localhost:8080" : "";

const KEY = "constella_platform_token";

function read() {
  try { return localStorage.getItem(KEY) || ""; } catch { return ""; }
}

let token = read();

export function getToken() { return token; }

export function setToken(value) {
  token = value;
  try { localStorage.setItem(KEY, value); } catch { /* private mode: keep it in memory only */ }
}

/** An error answer of a service: {code, params} plus the HTTP status. */
export class ApiError extends Error {
  constructor(status, code, params) {
    super(`${status} ${code}`);
    this.status = status;
    this.code = code;
    this.params = params || {};
  }
}

export async function api(path, { method = "GET", body } = {}) {
  let resp;
  try {
    resp = await fetch(BASE + "/api" + path, {
      method,
      headers: {
        Authorization: "Bearer " + token,
        ...(body ? { "Content-Type": "application/json" } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new ApiError(0, "UNREACHABLE");
  }
  const text = await resp.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch { data = null; }
  }
  if (!resp.ok) throw new ApiError(resp.status, data?.code || "INTERNAL", data?.params);
  return data;
}

/** Opens the server-sent event stream; returns a function that closes it. */
export function openStream({ onEvent, onState }) {
  let source = null;
  let closed = false;
  let retry = null;

  const connect = () => {
    source = new EventSource(BASE + "/api/stream?token=" + encodeURIComponent(token));
    source.onopen = () => onState(true);
    source.onerror = () => {
      onState(false);
      source.close();
      if (!closed) retry = setTimeout(connect, 3000);
    };
    // Event types are routing keys such as "dataprep.build.completed".
    for (const type of STREAM_TYPES) {
      source.addEventListener(type, (e) => {
        try { onEvent(JSON.parse(e.data)); } catch { /* ignore a malformed frame */ }
      });
    }
  };
  connect();
  return () => { closed = true; clearTimeout(retry); source?.close(); };
}

const STREAM_TYPES = [
  "dataprep.build.started", "dataprep.build.completed", "dataprep.build.failed",
  "catalog.build.validated", "catalog.build.rejected",
  "release.requested", "release.published", "release.failed",
  "research.run.completed", "research.run.failed",
];

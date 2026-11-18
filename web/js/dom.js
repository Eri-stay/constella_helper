// Small DOM helpers. Text always goes through textContent, never innerHTML.

export const $ = (sel, root = document) => root.querySelector(sel);

export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) node.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    node.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return node;
}

export function badge(kind, text) {
  return el("span", { class: `badge s-${kind}` }, text);
}

export function fill(tbody, rows, columns, emptyText) {
  tbody.replaceChildren();
  if (!rows.length) {
    tbody.append(el("tr", {}, el("td", { class: "empty", colspan: columns }, emptyText)));
    return;
  }
  tbody.append(...rows);
}

const fmtTime = new Intl.DateTimeFormat("uk-UA", { dateStyle: "short", timeStyle: "medium" });

export const time = (iso) => (iso ? fmtTime.format(new Date(iso)) : "—");
export const int = (n) => (n || n === 0 ? Number(n).toLocaleString("uk-UA") : "—");

export function duration(start, end) {
  if (!start || !end) return "—";
  const s = (new Date(end) - new Date(start)) / 1000;
  return s < 60 ? `${s.toFixed(1)} с` : `${Math.floor(s / 60)} хв ${Math.round(s % 60)} с`;
}

export function bytes(n) {
  if (!n) return "—";
  return n < 1048576 ? `${(n / 1024).toFixed(0)} КБ` : `${(n / 1048576).toFixed(1)} МБ`;
}

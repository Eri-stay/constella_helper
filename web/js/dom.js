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

/**
 * Fills a table body. A row may carry `row.detailRow`, a full-width row that
 * follows it (an expandable explanation); sorting moves the two together.
 */
export function fill(tbody, rows, columns, emptyText) {
  tbody.replaceChildren();
  if (!rows.length) {
    tbody.append(el("tr", { class: "empty-row" }, el("td", { class: "empty", colspan: columns }, emptyText)));
    return;
  }
  for (const r of rows) {
    tbody.append(r);
    if (r.detailRow) tbody.append(r.detailRow);
  }
  const table = tbody.closest("table");
  if (table?._sort) applySort(table);
}

const collator = new Intl.Collator("uk", { numeric: true, sensitivity: "base" });

function cellValue(row, col) {
  const cell = row.children[col];
  if (!cell) return "";
  return cell.dataset.sort ?? cell.textContent.trim();
}

const asNumber = (v) => {
  const n = Number(String(v).replace(/[\s\u00a0]/g, "").replace(",", "."));
  return v !== "" && v !== "—" && Number.isFinite(n) ? n : NaN;
};

function applySort(table) {
  const { col, dir } = table._sort;
  const tbody = table.tBodies[0];
  const rows = [...tbody.children].filter((r) => !r.classList.contains("detail") && !r.classList.contains("empty-row"));
  rows.sort((a, b) => {
    const x = cellValue(a, col);
    const y = cellValue(b, col);
    const nx = asNumber(x);
    const ny = asNumber(y);
    const c = Number.isNaN(nx) || Number.isNaN(ny) ? collator.compare(x, y) : nx - ny;
    return c * dir;
  });
  for (const r of rows) {
    tbody.append(r);
    if (r.detailRow) tbody.append(r.detailRow);
  }
  [...table.tHead.rows[0].cells].forEach((th, i) =>
    th.setAttribute("aria-sort", i === col ? (dir > 0 ? "ascending" : "descending") : "none"));
}

/** Makes every non-empty header cell sort its column on click (and Enter). */
export function makeSortable(table) {
  [...table.tHead.rows[0].cells].forEach((th, i) => {
    if (!th.textContent.trim()) return;
    th.classList.add("sortable");
    th.tabIndex = 0;
    const toggle = () => {
      const same = table._sort?.col === i;
      table._sort = { col: i, dir: same ? -table._sort.dir : 1 };
      applySort(table);
    };
    th.addEventListener("click", toggle);
    th.addEventListener("keydown", (e) => { if (e.key === "Enter") toggle(); });
  });
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

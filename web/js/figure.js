// Draws one constellation figure as SVG. Stars sit on an equirectangular
// patch of the sky: east is to the left, as on a sky chart.

const NS = "http://www.w3.org/2000/svg";

function node(tag, attrs = {}) {
  const n = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
  return n;
}

const rad = (deg) => (deg * Math.PI) / 180;

/** Signed difference a−b of two angles in degrees, in (−180, 180]. */
function wrap(d) {
  const x = ((d + 180) % 360 + 360) % 360 - 180;
  return x === -180 ? 180 : x;
}

export function drawFigure(svg, fig) {
  svg.replaceChildren();
  const stars = fig.stars;
  if (!stars.length) return false;

  // Centre on the mean direction so figures that straddle RA 0h stay in one piece.
  const sx = stars.reduce((a, s) => a + Math.sin(rad(s.ra * 15)), 0);
  const sc = stars.reduce((a, s) => a + Math.cos(rad(s.ra * 15)), 0);
  const centre = (Math.atan2(sx, sc) * 180) / Math.PI;
  const decMid = stars.reduce((a, s) => a + s.dec, 0) / stars.length;
  const k = Math.max(0.2, Math.cos(rad(decMid)));

  const pos = new Map(stars.map((s) => [s.hip, { x: -wrap(s.ra * 15 - centre) * k, y: -s.dec, s }]));
  const xs = [...pos.values()].map((p) => p.x);
  const ys = [...pos.values()].map((p) => p.y);
  const [x0, x1, y0, y1] = [Math.min(...xs), Math.max(...xs), Math.min(...ys), Math.max(...ys)];
  const size = Math.max(x1 - x0, y1 - y0, 1);
  const pad = size * 0.12;
  svg.setAttribute("viewBox", `${x0 - pad} ${y0 - pad} ${x1 - x0 + 2 * pad} ${y1 - y0 + 2 * pad}`);

  for (const e of fig.lines) {
    const a = pos.get(e.a);
    const b = pos.get(e.b);
    if (!a || !b) continue;
    svg.append(node("line", { class: "edge", x1: a.x, y1: a.y, x2: b.x, y2: b.y, "vector-effect": "non-scaling-stroke" }));
  }
  const labelAll = stars.length <= 12;
  for (const p of pos.values()) {
    const r = size * 0.008 * (1 + Math.max(0, 5 - p.s.mag) * 0.35);
    svg.append(node("circle", { class: "star", cx: p.x, cy: p.y, r }));
    const name = p.s.name_uk || p.s.name || (p.s.bayer ? `${p.s.bayer} ${p.s.con}` : "");
    if (name && (labelAll || p.s.mag < 3.5)) {
      const t = node("text", { class: "label", x: p.x + r * 1.6, y: p.y - r * 1.2, "font-size": size * 0.035 });
      t.textContent = name;
      svg.append(t);
    }
  }
  return true;
}

// The services send codes and parameters, never text; this is the Ukrainian
// translation (same rule as the product: tr() keys, Ukrainian by default).

const STATUS = {
  running: "виконується", completed: "готово", failed: "помилка",
  validated: "перевірено", rejected: "відхилено",
  ok: "ок", warn: "увага", fail: "помилка",
};

const ERRORS = {
  UNAUTHORIZED: "Невірний токен доступу.",
  UNREACHABLE: "Немає зв'язку зі сервером.",
  UPSTREAM_UNAVAILABLE: "Сервіс недоступний.",
  CIRCUIT_OPEN: "Сервіс недоступний (автоматичний вимикач відкрито).",
  TIMEOUT: "Сервіс не відповів вчасно.",
  NOT_FOUND: "Не знайдено.",
  CONFLICT: "Конфлікт стану.",
  BAD_REQUEST: "Некоректний запит.",
  INTERNAL: "Внутрішня помилка сервісу.",
  NO_ACTIVE_CATALOG: "Ще немає жодної перевіреної збірки.",
  FAULT_INJECTED: "Збій, увімкнений навмисно.",
  BUILD_RUNNING: "Збірка вже виконується.",
  SOURCE_UNAVAILABLE: "Не вдалося прочитати вхідні дані.",
  WRITE_FAILED: "Не вдалося записати файл.",
  INTERRUPTED: "Збірку перервано перезапуском.",
  CANCELLED: "Збірку скасовано.",
  CATALOG_UNAVAILABLE: "catalog недоступний.",
  AUDIT_UNAVAILABLE: "telemetry недоступний (аудит обов'язковий).",
  SWITCH_FAILED: "Не вдалося перемкнути version.json.",
  BUILD_NOT_VALIDATED: "Збірку не перевірено.",
  BUILD_RESERVED: "Збірку вже зарезервовано іншим релізом.",
  BUILD_RELEASED: "Збірку вже опубліковано.",
  BUILD_NOT_RESERVED: "Резерв не належить цьому релізу.",
  RELEASE_FAILED: "Реліз не вдався.",
  MODE_NOT_IMPLEMENTED: "Цей режим ще не реалізовано.",
};

// Each check: a title and a function that renders its parameters.
const num = (n) => Number(n).toLocaleString("uk-UA");
const short = (h) => (h ? h.slice(0, 12) + "…" : "—");

const CHECKS = {
  SCHEMA_VERSION: {
    title: "Версія схеми cosmos.db",
    detail: (p) => `очікується ${p.expected}, у файлі ${p.actual}`,
  },
  ARTIFACT_SHA256: {
    title: "SHA-256 файлу збігається з оголошеним",
    detail: (p) => `${short(p.expected)} / ${short(p.actual)}`,
  },
  CONTENT_HASH: {
    title: "Хеш вмісту збігається з метаданими",
    detail: (p) => `${short(p.stored)} / ${short(p.computed)}`,
  },
  STAR_TIER: {
    title: (p) => `Зорі яскравіші за ${p.mag_limit}m`,
    detail: (p) => `очікується ${num(p.expected)}, є ${num(p.actual)}`,
  },
  CONSTELLATIONS: {
    title: "Кількість сузір'їв IAU",
    detail: (p) => `очікується ${p.expected}, є ${p.actual}`,
  },
  BRIGHTEST_STAR: {
    title: "Кожне сузір'я має найяскравішу зорю",
    detail: (p) => (p.missing ? `без зорі: ${p.missing}` : "усі на місці"),
  },
  CULTURES: {
    title: "Культури та їхні ребра є у файлі",
    detail: (p) => `культур ${p.cultures}, ребер ${num(p.edges)}`,
  },
  EDGE_STARS: {
    title: "Кожне ребро культури має HIP-зорю",
    detail: (p) => (p.without_star ? `кінців без зорі: ${p.without_star}` : "усі кінці знайдено"),
  },
  DROPPED_EDGES: {
    title: "Ребра, відкинуті при збірці",
    detail: (p) => (p.dropped ? `${p.dropped}: у джерелі є HIP, якого немає в HYG` : "немає"),
  },
  ARTIFACT_UNREADABLE: {
    title: "Файл збірки читається як cosmos.db",
    detail: () => "файл відсутній або це не cosmos.db",
  },
};

export const statusText = (s) => STATUS[s] || s;

export const errorText = (e) => {
  const code = e?.code ?? e;
  return ERRORS[code] || `Помилка: ${code}`;
};

export function checkTitle(c) {
  const spec = CHECKS[c.code];
  if (!spec) return c.code;
  return typeof spec.title === "function" ? spec.title(c.params || {}) : spec.title;
}

export function checkDetail(c) {
  const spec = CHECKS[c.code];
  return spec?.detail ? spec.detail(c.params || {}) : "";
}

const STEPS = {
  reserve: "резерв у catalog", switch: "перемикання version.json",
  mark_released: "позначка в catalog", audit: "аудит у telemetry",
};
export const stepText = (s) => STEPS[s] || s;

// Обёртка над fetch: относительные пути (панель может жить за прокси),
// пароль панели из sessionStorage в заголовке X-Panel-Password.

const PW_KEY = "panel_pw";
let onUnauthorized = () => {};

export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn;
}

export function password() {
  try { return sessionStorage.getItem(PW_KEY) || ""; } catch { return ""; }
}

export function setPassword(v) {
  try {
    if (v) sessionStorage.setItem(PW_KEY, v);
    else sessionStorage.removeItem(PW_KEY);
  } catch { /* приватный режим — живём без запоминания */ }
}

export class ApiError extends Error {
  constructor(message, status, body) {
    super(message);
    this.status = status;
    this.body = body;
  }
  get offline() { return this.status === 0; }
}

const FRIENDLY = {
  "store error": "База не ответила. Попробуйте ещё раз.",
  "bad json": "Сервер не понял запрос.",
  "lesson not found": "Эта пара пропала из расписания — обновите экран.",
  "url must be https://bbb.ssau.ru/b/...": "Нужна ссылка вида https://bbb.ssau.ru/b/…",
  "refresh unavailable": "Обновление расписания сейчас недоступно.",
};

export async function api(path, { method = "GET", body, signal } = {}) {
  const headers = { Accept: "application/json" };
  const pw = password();
  if (pw) headers["X-Panel-Password"] = pw;
  let payload;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json; charset=utf-8";
    payload = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path.replace(/^\//, ""), { method, headers, body: payload, signal, cache: "no-store" });
  } catch (e) {
    if (e && e.name === "AbortError") throw e;
    throw new ApiError("Нет связи с сервером.", 0, null);
  }
  const text = await res.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch { data = { error: text.trim() }; }
  }
  if (res.status === 401) {
    onUnauthorized();
    throw new ApiError("Нужен пароль панели.", 401, data);
  }
  if (!res.ok) {
    const raw = (data && data.error) || `Ошибка ${res.status}`;
    throw new ApiError(FRIENDLY[raw] || raw, res.status, data);
  }
  return data;
}

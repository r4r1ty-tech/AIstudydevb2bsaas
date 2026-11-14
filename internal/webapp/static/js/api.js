// Обёртка над fetch: относительные пути (панель может жить за прокси).
// Вход держится на HttpOnly-куке: пароль из бота уходит на сервер один раз
// (login), дальше браузер сам шлёт куку, а пароль нигде не хранится.

const LEGACY_PW_KEY = "panel_pw";
let onUnauthorized = () => {};

export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn;
}

// Раньше пароль лежал в sessionStorage. Забираем его один раз (чтобы открытая
// вкладка не потеряла вход при обновлении) и стираем.
export function takeLegacyPassword() {
  try {
    const v = sessionStorage.getItem(LEGACY_PW_KEY) || "";
    sessionStorage.removeItem(LEGACY_PW_KEY);
    return v;
  } catch { return ""; }
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

async function request(path, { method = "GET", body, signal, priority, password = "", quiet401 = false } = {}) {
  const headers = { Accept: "application/json" };
  if (password) headers["X-Panel-Password"] = password;
  let payload;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json; charset=utf-8";
    payload = JSON.stringify(body);
  }
  let res;
  try {
    const init = { method, headers, body: payload, signal, cache: "no-store" };
    // priority: "low" — фоновые опросы не отбирают канал у действий человека.
    if (priority) init.priority = priority;
    res = await fetch(path.replace(/^\//, ""), init);
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
    if (!quiet401) onUnauthorized();
    throw new ApiError("Нужен пароль панели.", 401, data);
  }
  if (!res.ok) {
    const raw = (data && data.error) || `Ошибка ${res.status}`;
    throw new ApiError(FRIENDLY[raw] || raw, res.status, data);
  }
  return data;
}

export function api(path, opts = {}) {
  const { method, body, signal, priority } = opts;
  return request(path, { method, body, signal, priority });
}

// login меняет пароль из бота на долгую сессию в куке. 401 здесь — не «сессия
// кончилась», а «пароль не подошёл», поэтому общий обработчик не вызываем.
export function login(password) {
  return request("api/login", { method: "POST", password, quiet401: true });
}

// logout удаляет сессию на сервере и стирает куку.
export function logout() {
  return request("api/logout", { method: "POST", quiet401: true });
}

// Точка входа: вход, навигация по hash (#today, #week, #due, #notes, #more),
// совместимость с Telegram Mini App без блокировки загрузки.
import { api, password, setPassword, setUnauthorizedHandler } from "./api.js";
import { html, setHTML } from "./dom.js";
import { icon } from "./icons.js";
import { setTimezone } from "./format.js";
import { app } from "./ui.js";
import { mountToday } from "./views/today.js";
import { mountWeek } from "./views/week.js";
import { mountDue } from "./views/due.js";
import { mountNotes } from "./views/notes.js";
import { mountMore } from "./views/more.js";

const ROUTES = {
  today: { label: "Сегодня", ico: "today", mount: mountToday, wide: true },
  week: { label: "Неделя", ico: "week", mount: mountWeek },
  due: { label: "Сдать", ico: "due", mount: mountDue },
  notes: { label: "Конспекты", ico: "notes", mount: mountNotes },
  more: { label: "Ещё", ico: "more", mount: mountMore },
};

const gate = document.getElementById("gate");
const setup = document.getElementById("setup");
const shell = document.getElementById("app");
let screen = document.getElementById("screen");
const badges = {};
let unmount = null;
let life = null; // AbortController текущего экрана
let current = "";
let booted = false;

// Telegram: скрипт грузим только внутри Telegram и асинхронно — в обычном
// браузере страница не ждёт внешних запросов.
function telegram() {
  const inTG = /tgWebApp/.test(location.hash + location.search) || "TelegramWebviewProxy" in window;
  if (!inTG) return;
  const s = document.createElement("script");
  s.src = "https://telegram.org/js/telegram-web-app.js";
  s.async = true;
  s.onload = () => {
    const tg = window.Telegram && window.Telegram.WebApp;
    if (!tg) return;
    try {
      tg.ready();
      tg.expand();
      const bg = getComputedStyle(document.documentElement).getPropertyValue("--bg").trim() || "#090c0a";
      tg.setHeaderColor(bg);
      tg.setBackgroundColor(bg);
    } catch { /* старый клиент Telegram */ }
    if (/tgWebApp/.test(location.hash)) history.replaceState(null, "", "#today");
  };
  document.head.append(s);
}

function parseHash() {
  const raw = location.hash.replace(/^#/, "");
  const [name, ...rest] = raw.split("/");
  return { name: ROUTES[name] ? name : "today", sub: decodeURIComponent(rest.join("/") || "") };
}

function renderNav() {
  setHTML(document.getElementById("nav-links"), html`${Object.entries(ROUTES).map(([id, r]) => html`<a class="nav-link" href="#${id}" data-route="${id}" ${id === current ? html`aria-current="page"` : ""}>
      <span class="nav-ico">${icon(r.ico)}</span><span class="nav-label">${r.label}</span>
      ${badges[id] ? html`<span class="nav-badge" aria-label="${badges[id]} просрочено">${badges[id]}</span>` : ""}
    </a>`)}`);
  document.getElementById("nav-foot").textContent = app.settings.demo ? "Демо: тестовые данные, изменения живут до перезапуска." : "";
}

function setBadge(id, n) {
  badges[id] = n > 0 ? n : 0;
  renderNav();
}

async function refreshBadge() {
  try {
    const d = await api("api/deadlines");
    setBadge("due", d.counts ? d.counts.overdue : 0);
  } catch { /* бейдж необязателен */ }
}

// leaveScreen снимает всё, что повесил экран: таймеры (через unmount), обработчики
// на document (через signal) и обработчики на корне — корень меняется на новый узел.
function leaveScreen() {
  if (life) life.abort();
  life = null;
  if (unmount) unmount();
  unmount = null;
  const fresh = document.createElement("div");
  fresh.id = "screen";
  screen.replaceWith(fresh);
  screen = fresh;
}

function route() {
  const { name, sub } = parseHash();
  const changed = name !== current;
  leaveScreen();
  life = new AbortController();
  current = name;
  renderNav();
  screen.className = `screen${ROUTES[name].wide ? " wide" : ""}`;
  document.title = `${ROUTES[name].label} · Пульт`;
  unmount = ROUTES[name].mount(screen, { sub, setBadge, refreshBadge, signal: life.signal }) || null;
  if (changed) {
    window.scrollTo(0, 0);
    requestAnimationFrame(() => {
      const h = screen.querySelector(".screen-title");
      if (h && document.activeElement && document.activeElement.closest(".nav")) h.focus({ preventScroll: true });
    });
  }
}

function showGate(msg) {
  booted = false;
  leaveScreen();
  current = "";
  shell.hidden = true;
  setup.hidden = true;
  gate.hidden = false;
  const p = document.getElementById("gate-msg");
  p.hidden = !msg;
  p.textContent = msg || "";
  const inp = document.getElementById("pw");
  inp.value = "";
  inp.focus();
}

async function probeIllustrations() {
  for (const [key, file] of [["empty", "static/img/empty.webp"], ["moodle", "static/img/moodle.webp"]]) {
    try {
      const r = await fetch(file, { method: "HEAD" });
      if (r.ok) app.illus[key] = file;
    } catch { /* без картинки — значок */ }
  }
}

async function boot() {
  let s;
  try {
    s = await api("api/settings");
  } catch (e) {
    if (e.status === 401) {
      showGate(password() ? "Пароль не подошёл." : "");
      return;
    }
    shell.hidden = false;
    gate.hidden = true;
    leaveScreen();
    setHTML(screen, html`<div class="empty is-error" role="alert"><span class="empty-ico">${icon("wifioff", "icon-lg")}</span>
      <p class="empty-title">Панель не отвечает</p><p class="empty-text">${e.message}</p>
      <button type="button" class="btn" id="boot-retry">${icon("refresh")}Повторить</button></div>`);
    document.getElementById("boot-retry").addEventListener("click", boot);
    return;
  }
  app.settings = s;
  setTimezone(s.timezone);
  gate.hidden = true;
  // Первый вход: пока нет имени для BBB и подгруппы, приложение не открываем.
  if (!s.demo) {
    try {
      const me = await api("api/me");
      if (!me.onboarded || !me.fio || !me.subgroup) {
        showSetup(me);
        return;
      }
    } catch { /* профиль не прочитался — пускаем, поправят в «Ещё» */ }
  }
  setup.hidden = true;
  booted = true;
  shell.hidden = false;
  await probeIllustrations();
  route();
  refreshBadge();
}

function showSetup(me) {
  shell.hidden = true;
  setup.hidden = false;
  const form = document.getElementById("setup-form");
  form.querySelector("#setup-fio").value = me.fio || "";
  for (const b of form.querySelectorAll("[data-sub]")) b.setAttribute("aria-checked", String(Number(b.dataset.sub) === me.subgroup));
  form.querySelector("#setup-fio").focus();
}

function setupError(msg) {
  const p = document.getElementById("setup-msg");
  p.textContent = msg;
  p.hidden = !msg;
}

document.getElementById("setup-form").addEventListener("click", (ev) => {
  const b = ev.target.closest("[data-sub]");
  if (!b) return;
  for (const x of b.parentElement.querySelectorAll("[data-sub]")) x.setAttribute("aria-checked", String(x === b));
});

document.getElementById("setup-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const form = ev.currentTarget;
  const fio = form.querySelector("#setup-fio").value.trim().replace(/\s+/g, " ");
  const sel = form.querySelector('[data-sub][aria-checked="true"]');
  if (fio.split(" ").length < 2) return setupError("Впишите хотя бы фамилию и имя.");
  if (!sel) return setupError("Выберите подгруппу.");
  setupError("");
  const btn = form.querySelector('[type="submit"]');
  btn.disabled = true;
  try {
    await api("api/me", { method: "POST", body: { fio, subgroup: Number(sel.dataset.sub), onboard: true } });
    boot();
  } catch (e) {
    setupError(e.message || "Не сохранилось, попробуйте ещё раз.");
  } finally {
    btn.disabled = false;
  }
});

setUnauthorizedHandler(() => {
  if (!booted) return;
  setPassword("");
  showGate("Пароль сменился — войдите заново.");
});

document.getElementById("login-form").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const v = document.getElementById("pw").value.trim();
  if (!v) {
    showGate("Введите пароль.");
    return;
  }
  setPassword(v);
  boot();
});

window.addEventListener("hashchange", () => {
  if (!booted) return;
  const { name } = parseHash();
  // #week/2026-11-12 меняет только день внутри экрана — не перемонтируем.
  if (name === current && name === "week") return;
  route();
});

telegram();
boot();

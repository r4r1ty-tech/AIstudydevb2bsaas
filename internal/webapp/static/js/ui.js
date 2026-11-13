// Общие куски разметки: шапка экрана, скелетоны, пустые состояния, ошибки.
import { html } from "./dom.js";
import { icon } from "./icons.js";

export const app = {
  settings: { demo: false, timezone: "Europe/Samara" },
  offline: false,
  illus: { empty: "", moodle: "" },
};

export function head({ eyebrow = "", title, side = "" }) {
  return html`<header class="screen-head">
    <div class="screen-head-text">
      ${eyebrow ? html`<p class="eyebrow">${eyebrow}</p>` : ""}
      <h1 class="screen-title" tabindex="-1">${title}</h1>
    </div>
    <div class="head-side">${side}${app.offline ? html`<span class="pill pill-offline">${icon("wifioff", "icon-sm")}нет связи</span>` : ""}${app.settings.demo ? html`<span class="pill pill-demo" title="Панель показывает тестовые данные">демо-данные</span>` : ""}</div>
  </header>`;
}

export function skeleton(kind = "list") {
  if (kind === "today") {
    return html`<div class="stack-lg" aria-busy="true" aria-label="Загрузка">
      <div class="sk sk-line" style="width:40%"></div>
      <div class="sk sk-title"></div>
      <div class="sk" style="height:58px"></div>
      <div class="sk sk-hero"></div>
      <div class="sk sk-row"></div>
      <div class="sk sk-row"></div>
    </div>`;
  }
  return html`<div class="stack-lg" aria-busy="true" aria-label="Загрузка">
    <div class="sk sk-line" style="width:35%"></div>
    <div class="sk sk-title"></div>
    <div class="sk sk-block"></div>
    <div class="sk sk-row"></div>
    <div class="sk sk-row"></div>
    <div class="sk sk-row"></div>
  </div>`;
}

export function empty({ ico = "calendar", title, text = "", action = "", img = "" }) {
  return html`<div class="empty">
    ${img ? html`<img class="illus" src="${img}" alt="" width="640" height="360" loading="lazy">` : html`<span class="empty-ico">${icon(ico, "icon-lg")}</span>`}
    <p class="empty-title">${title}</p>
    ${text ? html`<p class="empty-text">${text}</p>` : ""}
    ${action}
  </div>`;
}

export function errorBlock(err, retryId = "retry") {
  const offline = err && err.status === 0;
  return html`<div class="empty is-error" role="alert">
    <span class="empty-ico">${icon(offline ? "wifioff" : "warn", "icon-lg")}</span>
    <p class="empty-title">${offline ? "Нет связи с панелью" : "Не получилось загрузить"}</p>
    <p class="empty-text">${offline ? "Проверьте интернет — данные подтянутся, как только связь вернётся." : (err && err.message) || "Что-то пошло не так."}</p>
    <button type="button" class="btn" data-action="${retryId}">${icon("refresh")}Повторить</button>
  </div>`;
}

export function kindLabel(kind, type) {
  switch (kind) {
    case "lecture": return "Лекция";
    case "practice": return "Практика";
    case "lab": return "Лабораторная";
    default: return type || "Занятие";
  }
}

export function busy(btn, on) {
  if (!btn) return;
  btn.classList.toggle("is-busy", on);
  btn.disabled = on;
  if (on) btn.setAttribute("aria-busy", "true");
  else btn.removeAttribute("aria-busy");
}

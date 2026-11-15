// Тосты. Подтверждения («Сохранено») гаснут сами через пару секунд, ошибки
// висят, пока их не закроют крестиком: за 5 секунд их легко не дочитать.
// Озвучка идёт через отдельную скрытую live-область (или ariaNotify), чтобы
// читалка произносила только текст, без «Закрыть».
import { html, str } from "./dom.js";
import { icon } from "./icons.js";

const MAX = 3;
const box = () => document.getElementById("toasts");

function announce(message) {
  if (typeof document.ariaNotify === "function") {
    try { document.ariaNotify(message); return; } catch { /* ниже — запасной путь */ }
  }
  const live = document.getElementById("toast-live");
  if (!live) return;
  live.textContent = "";
  // Пауза нужна, чтобы одинаковый текст подряд тоже прочитался.
  setTimeout(() => { live.textContent = message; }, 50);
}

// dismiss убирает тост; если фокус был на его крестике, переводим фокус на
// соседний тост или на основной экран, а не роняем его на <body>.
function dismiss(el) {
  if (!el.isConnected) return;
  const hadFocus = el.contains(document.activeElement);
  const next = el.nextElementSibling || el.previousElementSibling;
  el.remove();
  if (!hadFocus) return;
  const target = (next && next.querySelector(".toast-close")) || document.getElementById("main");
  if (target) target.focus({ preventScroll: true });
}

export function toast(message, kind = "ok") {
  const root = box();
  if (!root) return;
  const text = String(message || "");
  // Одинаковая ошибка уже висит — не плодим копии, только озвучиваем снова.
  for (const t of root.children) {
    if (t.dataset.kind === kind && t.dataset.text === text) {
      announce(text);
      return;
    }
  }
  const el = document.createElement("div");
  el.className = `toast toast-${kind}`;
  el.dataset.kind = kind;
  el.dataset.text = text;
  const close = kind === "error"
    ? html`<button type="button" class="toast-close" aria-label="Закрыть уведомление">${icon("x")}</button>`
    : "";
  el.innerHTML = str(html`${icon(kind === "error" ? "warn" : "check")}<span class="toast-text">${text}</span>${close}`);
  root.append(el);
  while (root.children.length > MAX) dismiss(root.firstElementChild);
  announce(text);
  if (kind === "error") {
    el.querySelector(".toast-close").addEventListener("click", () => dismiss(el));
  } else {
    setTimeout(() => dismiss(el), 2600);
  }
}

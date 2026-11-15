// Мини-шаблонизатор: html`` экранирует подстановки, raw() пропускает готовую разметку.

const RAW = Symbol("raw");

export function raw(s) {
  return { [RAW]: String(s == null ? "" : s) };
}

export function esc(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function part(v) {
  if (v == null || v === false || v === true) return "";
  if (Array.isArray(v)) return v.map(part).join("");
  if (typeof v === "object" && RAW in v) return v[RAW];
  return esc(v);
}

export function html(strings, ...vals) {
  let out = strings[0];
  for (let i = 0; i < vals.length; i++) out += part(vals[i]) + strings[i + 1];
  return raw(out);
}

export function str(r) {
  return part(r);
}

export function $(sel, root = document) {
  return root.querySelector(sel);
}

function fromHTML(markup) {
  const t = document.createElement("template");
  t.innerHTML = markup.trim();
  return t.content.firstElementChild;
}

// typingIn: внутри узла фокус в поле ввода — такой узел не пересоздаём,
// чтобы не потерять набранное.
function typingIn(el) {
  const a = document.activeElement;
  return !!(a && el.contains(a) && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName));
}

// focusKey: если фокус на кнопке с data-join/data-mode, после замены узла вернём его.
function focusKey(el) {
  const a = document.activeElement;
  if (!a || !el.contains(a)) return "";
  for (const k of ["join", "mode", "day", "gotoLink"]) {
    if (a.dataset && a.dataset[k] !== undefined) {
      const attr = k.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase());
      return `[data-${attr}="${CSS.escape(a.dataset[k])}"]`;
    }
  }
  return "";
}

// patch обновляет список по ключам: неизменённые узлы не трогает, не
// пересоздаёт узел, внутри которого сейчас фокус (поле ввода ссылки), и не
// сбрасывает прокрутку. items: [{key, html}] — html это результат html``.
export function patch(container, items) {
  const old = new Map();
  for (const el of Array.from(container.children)) {
    if (el.dataset.key) old.set(el.dataset.key, el);
    else el.remove();
  }
  let ref = container.firstElementChild;
  for (const it of items) {
    const markup = str(it.html);
    let el = old.get(it.key);
    if (el) {
      old.delete(it.key);
      if (el._markup !== markup && !typingIn(el)) {
        const focusSel = focusKey(el);
        const next = fromHTML(markup);
        next.dataset.key = it.key;
        next._markup = markup;
        if (el.tagName === "DETAILS" && next.tagName === "DETAILS") next.open = el.open;
        const wasRef = el === ref;
        el.replaceWith(next);
        el = next;
        if (wasRef) ref = next;
        if (focusSel) {
          const f = next.querySelector(focusSel);
          if (f) f.focus({ preventScroll: true });
        }
      }
    } else {
      el = fromHTML(markup);
      el.dataset.key = it.key;
      el._markup = markup;
    }
    if (el !== ref) container.insertBefore(el, ref);
    ref = el.nextElementSibling;
  }
  for (const el of old.values()) el.remove();
}

export function setHTML(el, r) {
  el.innerHTML = str(r);
}

// safeHref разрешает только http:, https:, mailto:, якоря #... и относительные
// пути /... (кроме //). Опасные (javascript:, data:, vbscript:) и битые схемы -> "".
export function safeHref(u) {
  if (u == null) return "";
  const s = String(u).trim();
  if (!s || /[\u0000-\u001f\u007f]/.test(s)) return "";
  if (s.startsWith("#")) return s;
  if (s.startsWith("/") && !s.startsWith("//") && !s.startsWith("/\\")) return s;
  if (!/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(s)) return "";
  try {
    const base = typeof window !== "undefined" && window.location ? window.location.href : "https://localhost/";
    const parsed = new URL(s, base);
    if (parsed.protocol === "http:" || parsed.protocol === "https:" || parsed.protocol === "mailto:") {
      return s;
    }
  } catch {
    return "";
  }
  return "";
}

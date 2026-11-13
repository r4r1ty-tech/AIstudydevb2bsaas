// Карточка пары и всё, что с ней делают: переключатель «Заходить / Не заходить»
// с оптимистичным обновлением и вставка ссылки на комнату.
import { api } from "./api.js";
import { html } from "./dom.js";
import { icon } from "./icons.js";
import { duration, now } from "./format.js";
import { app, kindLabel } from "./ui.js";
import { toast } from "./toast.js";

export function willJoin(v, me) {
  return !!(v.online && v.mine && me && me.active && v.decision !== "no" && v.phase !== "past");
}

// Режим дня по онлайн-парам моей подгруппы — как dayMode на сервере.
export function dayMode(lessons) {
  const pool = lessons.filter((v) => v.online && v.mine);
  if (!pool.length) return "empty";
  const live = pool.filter((v) => v.phase !== "past");
  const use = live.length ? live : pool;
  const first = use[0].decision;
  if (use.some((v) => v.decision !== first)) return "mixed";
  return first === "yes" ? "all" : first === "no" ? "none" : "auto";
}

export function joinHint(v, me) {
  if (me && !me.active) return "бот сейчас выключен для вас";
  if (v.decision === "no") return v.phase === "now" ? "бот не будет на этой паре" : "бот пропустит эту пару";
  if (!v.has_bbb) return "но без ссылки бот не зайдёт";
  if (v.phase === "now") {
    const st = v.presence && v.presence.state;
    if (st === "room") return "бот на паре";
    if (st === "lobby") return "бот ждёт в лобби";
    return "бот заходит";
  }
  if (v.decision === "yes") return "зайдёт за 15 минут до начала";
  return "как обычно: зайдёт за 5 минут";
}

export function joinShort(v, me) {
  if (!v.online) return "очная, бот не нужен";
  if (!v.mine) return "не ваша подгруппа";
  if (me && !me.active) return "бот выключен";
  if (v.decision === "no") return "бот не пойдёт";
  if (!v.has_bbb) return "нет ссылки на комнату";
  if (v.decision === "yes") return "бот зайдёт за 15 мин";
  return "бот зайдёт сам";
}

// blocked: решение «заходить» есть, но бот не зайдёт (нет ссылки или бот выключен).
export function joinBlocked(v, me) {
  return v.decision !== "no" && (!v.has_bbb || !(me && me.active));
}

export function joinToggle(v, me, { compact = false, pending = false } = {}) {
  const on = v.decision !== "no";
  const warn = on && joinBlocked(v, me);
  const name = `Заходить на пару: ${v.discipline}, ${v.start}–${v.end}`;
  return html`<button type="button" class="join${compact ? " compact" : ""}${pending ? " is-pending" : ""}${warn ? " is-warn" : ""}" role="switch" aria-checked="${on ? "true" : "false"}" aria-label="${name}" aria-describedby="jh-${v.id}-${compact ? "c" : "f"}" data-join="${v.id}"${pending ? html` aria-busy="true"` : ""}>
    <span class="join-track" aria-hidden="true"><span class="join-thumb">${icon(warn ? "warn" : on ? "check" : "x")}</span></span>
    <span class="join-text">
      <span class="join-label">${on ? "Заходить" : "Не заходить"}</span>
      <span class="join-hint" id="jh-${v.id}-${compact ? "c" : "f"}">${joinHint(v, me)}</span>
    </span>
  </button>`;
}

function linkFix(v) {
  const prefix = app.settings.bbb_prefix || "https://…/b/";
  return html`<div class="notice notice-amber">
    ${icon("nolink")}
    <div class="notice-body">
      <strong>Нет ссылки на комнату</strong>
      <span>Без неё бот не зайдёт. Ссылка запомнится для этого предмета.</span>
      <form class="inline-form" data-link-form="${v.id}">
        <label class="visually-hidden" for="lf-${v.id}">Ссылка на комнату</label>
        <input class="input" id="lf-${v.id}" name="url" type="url" inputmode="url" autocomplete="off" spellcheck="false" placeholder="${prefix}…" required>
        <button type="submit" class="btn btn-primary">Сохранить</button>
      </form>
    </div>
  </div>`;
}

export function presenceChip(v) {
  const st = v.presence && v.presence.state;
  if (st === "room") return html`<span class="chip chip-acc">${icon("bot")}бот в комнате</span>`;
  if (st === "lobby") return html`<span class="chip chip-amber">${icon("bot")}бот в лобби</span>`;
  if (st === "error") return html`<span class="chip chip-coral">${icon("warn")}ошибка захода</span>`;
  return "";
}

export function lessonCard(v, me, { compact = false, controls = true, pending = false } = {}) {
  const cls = ["lesson"];
  if (compact) cls.push("compact");
  if (v.phase === "now") cls.push("is-now");
  if (v.phase === "past") cls.push("is-past");
  if (!v.online) cls.push("is-offline");
  if (!v.mine) cls.push("is-other");
  const showToggle = controls && v.online && v.mine && v.phase !== "past";
  const needLink = v.online && v.mine && !v.has_bbb && v.phase !== "past";
  let nowChip = "";
  if (v.phase === "now") {
    const left = Date.parse(v.finish) - now();
    nowChip = html`<span class="chip chip-solid">идёт · ещё ${duration(left)}</span>`;
  }
  return html`<article class="${cls.join(" ")}" id="lesson-${v.id}" aria-label="${v.start}–${v.end}, ${v.discipline}">
    <div class="lesson-time" aria-hidden="true">
      <span class="t-start">${v.start}</span>
      <span class="t-end">${v.end}</span>
      <span class="t-rail"></span>
    </div>
    <div class="lesson-body">
      <h3 class="lesson-title">${v.discipline}</h3>
      <div class="lesson-meta">
        ${nowChip}
        <span class="chip">${icon(v.kind)}${kindLabel(v.kind, v.type)}</span>
        ${v.online
          ? html`<span class="chip chip-ghost">${icon("online")}онлайн${v.has_bbb ? "" : html` · <span>без ссылки</span>`}</span>`
          : html`<span class="chip chip-ghost">${icon("place")}${v.place || "очно"}</span>`}
        ${v.subgroup && !v.mine ? html`<span class="chip chip-ghost">подгр. ${v.subgroup} · не ваша</span>` : ""}
        ${v.phase === "now" ? presenceChip(v) : ""}
        ${v.recording ? html`<span class="chip chip-coral"><span class="rec-dot" aria-hidden="true"></span>запись</span>` : ""}
        ${v.phase === "past" ? html`<span class="chip chip-ghost">${icon("check")}прошла</span>` : ""}
      </div>
      ${v.teacher ? html`<p class="lesson-teacher">${v.teacher}</p>` : ""}
    </div>
    ${showToggle || needLink
      ? html`<div class="lesson-foot">${showToggle ? joinToggle(v, me, { compact: true, pending }) : ""}${needLink ? linkFix(v) : ""}</div>`
      : ""}
  </article>`;
}

// Контроллер решений. view: { find(id), me(), base(id), render(), merge(resp), busy(bool) }.
export function bindLessonActions(root, view) {
  const pending = new Map();
  const lastTap = new Map();

  root.addEventListener("click", async (ev) => {
    const btn = ev.target.closest("[data-join]");
    if (!btn) return;
    const id = Number(btn.dataset.join);
    if (pending.has(id)) return; // запрос по этой паре ещё в полёте — второй не шлём
    // Случайный двойной тап не должен вернуть переключатель обратно.
    if (Date.now() - (lastTap.get(id) || 0) < 450) return;
    lastTap.set(id, Date.now());
    const v = view.find(id);
    if (!v) return;
    const prev = v.decision;
    const next = prev === "no" ? view.base(id) : "no";
    v.decision = next;
    v.will_join = willJoin(v, view.me());
    pending.set(id, next);
    view.busy(true);
    view.render();
    try {
      const res = await api("api/intent", { method: "POST", body: { lesson_id: id, decision: next } });
      pending.delete(id);
      view.merge(res);
    } catch (e) {
      pending.delete(id);
      v.decision = prev;
      v.will_join = willJoin(v, view.me());
      view.render();
      toast(`Не сохранилось: ${e.message}`, "error");
    } finally {
      view.busy(false);
    }
  });

  root.addEventListener("click", (ev) => {
    const g = ev.target.closest("[data-goto-link]");
    if (!g) return;
    const input = root.querySelector(`#lf-${g.dataset.gotoLink}`);
    if (!input) return;
    input.closest(".lesson").scrollIntoView({ block: "center", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
    input.focus({ preventScroll: true });
  });

  root.addEventListener("submit", async (ev) => {
    const form = ev.target.closest("[data-link-form]");
    if (!form) return;
    ev.preventDefault();
    const id = Number(form.dataset.linkForm);
    const input = form.querySelector("input");
    const url = (input.value || "").trim();
    const prefix = app.settings.bbb_prefix || "";
    if (!url || (prefix && !url.startsWith(prefix))) {
      toast(prefix ? `Нужна ссылка вида ${prefix}…` : "Вставьте ссылку на комнату", "error");
      input.focus();
      return;
    }
    const btn = form.querySelector("button");
    btn.classList.add("is-busy");
    btn.disabled = true;
    try {
      await api("api/bbb", { method: "POST", body: { lesson_id: id, url } });
      input.blur();
      toast("Ссылка сохранена");
      await view.reload();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      btn.classList.remove("is-busy");
      btn.disabled = false;
    }
  });

  return { pending };
}

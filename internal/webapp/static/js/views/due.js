// «Сдать»: дедлайны из Moodle по группам срочности.
import { api } from "../api.js";
import { html, setHTML } from "../dom.js";
import { icon } from "../icons.js";
import { ago, duration, now, syncClock, whenLabel } from "../format.js";
import { app, empty, errorBlock, head, skeleton } from "../ui.js";

const GROUPS = [
  { id: "overdue", title: "Просрочено", ico: "warn" },
  { id: "soon", title: "Сегодня и завтра", ico: "hourglass" },
  { id: "week", title: "На этой неделе", ico: "calendar" },
  { id: "later", title: "Позже", ico: "clock" },
];

function statusChip(s) {
  if (s === "submitted") return html`<span class="chip chip-acc">${icon("check")}Сдано</span>`;
  if (s === "draft") return html`<span class="chip chip-amber">${icon("file")}Файл прикреплён, не отправлено</span>`;
  return html`<span class="chip chip-ghost">${icon("x")}Не сдано</span>`;
}

function item(d) {
  const ms = Date.parse(d.due) - now();
  const left = d.group === "done" ? "" : ms < 0 ? `просрочено на ${duration(-ms)}` : `осталось ${duration(ms)}`;
  return html`<article class="due-item ${d.group}">
    <p class="due-course">${d.course}</p>
    <h3 class="due-title">${d.title}</h3>
    <div class="due-when"><span>${icon("clock", "icon-sm")} до ${whenLabel(d.due)}</span>${left ? html`<span class="due-left">${left}</span>` : ""}</div>
    <div class="due-foot">
      <div class="row">
        <span class="chip">${icon(d.kind === "quiz" ? "quiz" : "assign")}${d.kind === "quiz" ? "Тест" : "Задание"}</span>
        ${statusChip(d.status)}
      </div>
      ${d.url ? html`<a class="link-btn" href="${d.url}" target="_blank" rel="noopener noreferrer">Открыть в Moodle${icon("ext", "icon-sm")}</a>` : ""}
    </div>
  </article>`;
}

export function mountDue(root, ctx) {
  let alive = true;
  setHTML(root, skeleton());

  async function load() {
    let d;
    try {
      d = await api("api/deadlines");
      syncClock(d.now);
    } catch (e) {
      if (e.status === 401 || !alive) return;
      setHTML(root, html`${head({ title: "Сдать" })}${errorBlock(e)}`);
      return;
    }
    if (!alive) return;
    ctx.setBadge("due", d.counts ? d.counts.overdue : 0);
    const m = d.moodle;
    if (!m.connected) {
      setHTML(root, html`${head({ eyebrow: "Moodle не подключён", title: "Сдать" })}
        <section class="connect">
          ${app.illus.moodle ? html`<img class="illus" src="${app.illus.moodle}" alt="" width="640" height="360">` : html`<span class="empty-ico">${icon("key", "icon-lg")}</span>`}
          <h2>Подключите аккаунт универа</h2>
          <p>Панель сама заберёт из Moodle тесты и задания со сроками и разложит их по срочности: что горит, что на неделе, что потом.</p>
          <a class="btn btn-primary" href="#more/account">${icon("key")}Подключить Moodle</a>
          ${m.message ? html`<p class="field-hint">${m.message}</p>` : ""}
        </section>`);
      return;
    }
    const by = {};
    for (const x of d.deadlines) (by[x.group] = by[x.group] || []).push(x);
    const open = d.deadlines.filter((x) => x.group !== "done").length;
    const c = d.counts || {};
    const groups = GROUPS.filter((g) => by[g.id] && by[g.id].length);
    setHTML(root, html`${head({ eyebrow: html`Moodle · ${m.synced_at ? `обновлено ${ago(m.synced_at)}` : "подключён"}`, title: "Сдать" })}
      <div class="due-summary">
        <button type="button" class="due-stat${c.overdue ? " is-coral" : ""}" data-jump="overdue" ${c.overdue ? "" : "disabled"} aria-label="Просрочено: ${c.overdue || 0}. Показать"><b>${c.overdue || 0}</b><span>просрочено</span></button>
        <button type="button" class="due-stat${c.soon ? " is-amber" : ""}" data-jump="soon" ${c.soon ? "" : "disabled"} aria-label="Сегодня и завтра: ${c.soon || 0}. Показать"><b>${c.soon || 0}</b><span>сегодня и завтра</span></button>
        <button type="button" class="due-stat" data-jump="open" ${open ? "" : "disabled"} aria-label="Всего не сдано: ${open}. Показать"><b>${open}</b><span>всего не сдано</span></button>
      </div>
      ${groups.length ? "" : empty({ ico: "check", title: "Всё сдано", text: "Открытых сроков нет. Новые появятся здесь сами." })}
      ${groups.map((g) => html`<section class="due-group ${g.id}" id="due-${g.id}" aria-labelledby="dg-${g.id}">
        <h2 class="due-group-title" id="dg-${g.id}">${icon(g.ico)}${g.title} <span class="count mono">· ${by[g.id].length}</span></h2>
        ${by[g.id].map(item)}
      </section>`)}
      ${by.done && by.done.length ? html`<details class="fold"><summary>${icon("check")}Сдано · ${by.done.length}${icon("chev", "chev")}</summary><div class="fold-body">${by.done.map(item)}</div></details>` : ""}`);
  }

  // Плавная прокрутка не двигается, если у страницы нет кадров (вкладка в фоне,
  // скрытая панель): браузер просто стоит на месте. Поэтому через 350 мс проверяем
  // и, если страница так и не тронулась, переходим сразу.
  let jumpTimer = 0;
  ctx.signal.addEventListener("abort", () => clearTimeout(jumpTimer));

  function jumpTo(target) {
    const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const max = Math.max(0, document.documentElement.scrollHeight - window.innerHeight);
    // Заголовок оказывается у верхнего края. Снизу страница заканчивается
    // отступом под нижнюю панель (padding у .main), так что панель его не закрывает.
    const top = Math.min(max, Math.max(0, target.getBoundingClientRect().top + window.scrollY - 12));
    const from = window.scrollY;
    window.scrollTo({ top, behavior: reduce ? "auto" : "smooth" });
    clearTimeout(jumpTimer);
    if (!reduce && Math.abs(top - from) > 2) {
      jumpTimer = setTimeout(() => {
        if (Math.abs(window.scrollY - from) < 1) window.scrollTo({ top, behavior: "auto" });
      }, 350);
    }
    const h = target.querySelector(".due-group-title");
    if (!h) return;
    h.classList.remove("is-flash");
    void h.offsetWidth;
    h.classList.add("is-flash");
    h.setAttribute("tabindex", "-1");
    h.focus({ preventScroll: true });
  }

  root.addEventListener("click", (ev) => {
    const j = ev.target.closest("[data-jump]");
    if (j) {
      const target = j.dataset.jump === "open" ? root.querySelector(".due-group") : root.querySelector(`#due-${j.dataset.jump}`);
      if (target) jumpTo(target);
      return;
    }
    if (ev.target.closest('[data-action="retry"]')) {
      setHTML(root, skeleton());
      load();
    }
  }, { signal: ctx.signal });

  load();
  return () => { alive = false; };
}

// «Сдать»: дедлайны из Moodle по группам срочности.
import { api } from "../api.js";
import { html, safeHref, setHTML, str } from "../dom.js";
import { icon } from "../icons.js";
import { ago, duration, now, plural, syncClock, whenLabel } from "../format.js";
import { app, busy, empty, errorBlock, head, skeleton } from "../ui.js";
import { toast } from "../toast.js";
import { confirmDialog } from "../dialog.js";

const GROUPS = [
  { id: "notopen", title: "Скоро откроются", ico: "unlock" },
  { id: "overdue", title: "Просрочено", ico: "warn" },
  { id: "soon", title: "Сегодня и завтра", ico: "hourglass" },
  { id: "week", title: "На этой неделе", ico: "calendar" },
  { id: "later", title: "Позже", ico: "clock" },
  { id: "closed", title: "Закрыты, не пройдены", ico: "door" },
];

function statusChip(s, kind, closed) {
  if (s === "submitted") return html`<span class="chip chip-acc">${icon("check")}Сдано</span>`;
  if (s === "draft") return html`<span class="chip chip-amber">${icon("file")}${kind === "quiz" ? "Попытка не завершена" : "Черновик, не отправлено"}</span>`;
  if (closed) return html`<span class="chip chip-ghost">${icon("door")}Закрыт</span>`;
  return html`<span class="chip chip-ghost">${icon("x")}Не сдано</span>`;
}

// Текст письма преподавателю. Панель только готовит текст для копирования:
// ничего никуда не отправляет.
function letterText(items, fio) {
  const sign = fio ? `\n\n${fio}` : "";
  if (items.length === 1) {
    const d = items[0];
    const course = d.course ? ` по курсу «${d.course}»` : "";
    return `Здравствуйте!\n\nПрошу открыть повторную попытку или продлить срок для теста «${d.title}»${course}. Сейчас тест закрыт для попыток, а пройти его не получилось.\n\nСпасибо!${sign}`;
  }
  const list = items.map((d) => `— «${d.title}»${d.course ? `, курс «${d.course}»` : ""}`).join("\n");
  return `Здравствуйте!\n\nПрошу открыть повторную попытку или продлить срок для тестов, которые сейчас закрыты и не пройдены:\n${list}\n\nСпасибо!${sign}`;
}

function askPanel(items, fio) {
  const links = items.map((d) => ({ item: d, href: safeHref(d.url) })).filter((x) => x.href);
  return html`<div class="card card-pad stack ask-panel" role="region" aria-label="Письмо преподавателю">
    <div class="row"><span class="card-title">Письмо преподавателю</span><button type="button" class="btn btn-sm btn-ghost" data-ask-close>${icon("x")}Закрыть</button></div>
    <p class="field-hint">Текст можно поправить. Панель ничего не отправляет: скопируйте и отправьте сами.</p>
    ${fio ? "" : html`<p class="field-hint">Имя в профиле не задано: подпишите письмо сами.</p>`}
    <textarea class="input" rows="${Math.min(14, 7 + items.length)}" aria-label="Текст письма">${letterText(items, fio)}</textarea>
    <div class="btn-row"><button type="button" class="btn btn-primary btn-sm" data-copy-letter>${icon("file")}Скопировать</button>${links.map((x) => html`<a class="btn btn-sm" href="${x.href}" target="_blank" rel="noopener noreferrer">${icon("ext")}${links.length > 1 ? `Moodle: ${x.item.title}` : "Открыть в Moodle"}</a>`)}</div>
  </div>`;
}

// «Что сделать» — сворачиваемый блок, общий для обычной и «ещё не открытой» карточки.
function todoFold(d) {
  return d.todo ? html`<details class="fold"><summary>${icon("file")}Что сделать${icon("chev", "chev")}</summary><div class="fold-body"><ul class="due-todo">${d.todo.split("\n").filter(Boolean).map((t) => html`<li>${t}</li>`)}</ul></div></details>` : "";
}

// Карточка теста, который ещё не открылся: янтарная, без «Пройти»,
// с временем открытия и закрытия (окно). Сервер относит сюда только элементы с Opens в будущем.
function notOpenItem(d) {
  const hasOpens = !!d.opens && !d.opens.startsWith("0001-");
  const hasDue = !!d.due && !d.due.startsWith("0001-");
  const opensMs = hasOpens ? Date.parse(d.opens) : NaN;
  const dueMs = hasDue ? Date.parse(d.due) : NaN;
  const passed = hasOpens && opensMs <= now();
  const opensText = passed ? "открывается…" : `откроется ${whenLabel(d.opens)}`;
  const left = hasOpens && !passed ? `через ${duration(opensMs - now())}` : "";
  const win = hasOpens && hasDue && dueMs > opensMs ? ` · окно ${duration(dueMs - opensMs)}` : "";
  const closes = hasDue ? `закроется ${whenLabel(d.due)}${win}` : "";
  const href = safeHref(d.url);
  return html`<article class="due-item notopen">
    <p class="due-course">${d.course}${d.section ? html` · ${d.section}` : ""}</p>
    <h3 class="due-title">${d.title}</h3>
    <div class="due-when"><span class="chip chip-amber">${icon("unlock")}${opensText}</span>${left ? html`<span class="due-left">${left}</span>` : ""}</div>
    ${closes ? html`<div class="due-when"><span>${icon("clock", "icon-sm")} ${closes}</span></div>` : ""}
    <div class="due-foot">
      <div class="row">
        <span class="chip">${icon(d.kind === "quiz" ? "quiz" : "assign")}${d.kind === "quiz" ? "Тест" : "Задание"}</span>
        ${statusChip(d.status, d.kind, false)}
      </div>
      ${href ? html`<a class="link-btn" href="${href}" target="_blank" rel="noopener noreferrer">Открыть в Moodle${icon("ext", "icon-sm")}</a>` : ""}
    </div>
    ${d.kind === "quiz" && d.status !== "submitted" ? html`<div class="btn-row"><button type="button" class="btn btn-sm" disabled>${icon("clock")}ещё не открыт</button>
      <span class="field-hint">«Пройти» станет доступно, когда тест откроется</span></div>` : ""}
    ${todoFold(d)}
  </article>`;
}

function item(d) {
  if (d.group === "notopen") return notOpenItem(d);
  const hasDue = d.due && !d.due.startsWith("0001-");
  const ms = hasDue ? Date.parse(d.due) - now() : 0;
  const left = d.group === "done" || !hasDue ? "" : ms < 0 ? `просрочено на ${duration(-ms)}` : `осталось ${duration(ms)}`;
  const href = safeHref(d.url);
  return html`<article class="due-item ${d.group}">
    <p class="due-course">${d.course}${d.section ? html` · ${d.section}` : ""}</p>
    <h3 class="due-title">${d.title}</h3>
    <div class="due-when"><span>${icon("clock", "icon-sm")} ${hasDue ? html`до ${whenLabel(d.due)}` : "срок не указан"}</span>${left ? html`<span class="due-left">${left}</span>` : ""}</div>
    <div class="due-foot">
      <div class="row">
        <span class="chip">${icon(d.kind === "quiz" ? "quiz" : "assign")}${d.kind === "quiz" ? "Тест" : "Задание"}</span>
        ${statusChip(d.status, d.kind, d.closed)}
      </div>
      ${href ? html`<a class="link-btn" href="${href}" target="_blank" rel="noopener noreferrer">Открыть в Moodle${icon("ext", "icon-sm")}</a>` : ""}
    </div>
    ${d.kind === "quiz" && d.status !== "submitted" ? html`<div class="btn-row"><button type="button" class="btn btn-primary btn-sm" data-quiz-force="${d.id}">${icon("refresh")}Пройти</button>
      ${d.closed ? html`<button type="button" class="btn btn-sm" data-ask-open="${d.id}">${icon("file")}Попросить открыть</button>` : ""}
      <span class="field-hint">бот сам решит тест, даже просроченный, если Moodle ещё пускает</span></div>` : ""}
    ${todoFold(d)}
  </article>`;
}

export function mountDue(root, ctx) {
  let alive = true;
  let closedList = []; // закрытые тесты текущей выдачи: для кнопок «Попросить открыть»
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
    closedList = by.closed || [];
    // «Без срока» не горит: в счётчики не идёт, лежит отдельным блоком.
    const open = d.deadlines.filter((x) => x.group !== "done" && x.group !== "nodue").length;
    const nodue = by.nodue || [];
    const c = d.counts || {};
    const groups = GROUPS.filter((g) => by[g.id] && by[g.id].length);
    // Несданные тесты — те же, что получат «Пройти» у себя в карточке (сервер считает так же).
    // Ещё не открытые тесты в очередь не ставим: Moodle их не пустит (сервер считает так же).
    const queued = d.deadlines.filter((x) => x.kind === "quiz" && x.status !== "submitted" && !x.closed && !x.not_open);
    setHTML(root, html`${head({ eyebrow: html`Moodle · ${m.synced_at ? `обновлено ${ago(m.synced_at)}` : "подключён"}`, title: "Сдать" })}
      <div class="due-summary">
        <button type="button" class="due-stat${c.overdue ? " is-coral" : ""}" data-jump="overdue" ${c.overdue ? "" : "disabled"} aria-label="Просрочено: ${c.overdue || 0}. Показать"><b>${c.overdue || 0}</b><span>просрочено</span></button>
        <button type="button" class="due-stat${c.soon ? " is-amber" : ""}" data-jump="soon" ${c.soon ? "" : "disabled"} aria-label="Сегодня и завтра: ${c.soon || 0}. Показать"><b>${c.soon || 0}</b><span>сегодня и завтра</span></button>
        <button type="button" class="due-stat" data-jump="open" ${open ? "" : "disabled"} aria-label="Всего не сдано: ${open}. Показать"><b>${open}</b><span>всего не сдано</span></button>
      </div>
      ${queued.length ? html`<div class="btn-row due-all"><button type="button" class="btn btn-primary btn-sm" data-quiz-force-all data-count="${queued.length}">${icon("refresh")}Пройти все (${queued.length})</button></div>` : ""}
      ${groups.length ? "" : empty({ ico: "check", title: nodue.length ? "Горящих сроков нет" : "Всё сдано", text: nodue.length ? "Со сроками всё сдано. Ниже — задания без срока." : "Открытых сроков нет. Новые появятся здесь сами." })}
      ${groups.map((g) => html`<section class="due-group ${g.id}" id="due-${g.id}" aria-labelledby="dg-${g.id}">
        <h2 class="due-group-title" id="dg-${g.id}">${icon(g.ico)}${g.title} <span class="count mono">· ${by[g.id].length}</span></h2>
        ${g.id === "notopen" ? html`<p class="field-hint">Пройти заранее нельзя: Moodle пускает в тест только после открытия.</p>` : ""}
        ${g.id === "closed" ? html`<div class="btn-row"><button type="button" class="btn btn-sm" data-ask-group>${icon("file")}Попросить открыть все</button><span class="field-hint">один текст на все закрытые тесты</span></div>` : ""}
        ${by[g.id].map(item)}
      </section>`)}
      ${nodue.length ? html`<details class="fold due-nodue"><summary>${icon("file")}Без срока · ${nodue.length}${icon("chev", "chev")}</summary><div class="fold-body"><p class="field-hint">Срок в Moodle не указан — не горит, но сдать всё равно нужно.</p>${nodue.map(item)}</div></details>` : ""}
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

  // Письмо преподавателю: ФИО берём из профиля (api/me), текст только для копирования.
  function showPanel(host, items, fio) {
    root.querySelectorAll(".ask-panel").forEach((el) => el.remove());
    const t = document.createElement("template");
    t.innerHTML = str(askPanel(items, fio));
    const panel = t.content.firstElementChild;
    host.insertAdjacentElement("afterend", panel);
    panel.scrollIntoView({ block: "nearest" });
    panel.querySelector("textarea").focus({ preventScroll: true });
  }

  async function askOpen(btn, items) {
    busy(btn, true);
    let fio = "";
    try {
      const me = await api("api/me");
      fio = (me.fio || "").trim();
    } catch (e) {
      busy(btn, false);
      if (e.status !== 401 && alive) toast(e.message, "error");
      return;
    }
    busy(btn, false);
    if (!alive) return;
    showPanel(btn.closest("article") || btn.parentElement, items, fio);
  }

  async function copyLetter(btn) {
    const ta = btn.closest(".ask-panel").querySelector("textarea");
    let ok = false;
    try {
      await navigator.clipboard.writeText(ta.value);
      ok = true;
    } catch {
      ta.focus();
      ta.select();
      try { ok = document.execCommand("copy"); } catch { ok = false; }
    }
    toast(ok ? "Скопировано" : "Не вышло скопировать: выделите текст вручную", ok ? "ok" : "error");
  }

  root.addEventListener("click", async (ev) => {
    const qa = ev.target.closest("[data-quiz-force-all]");
    if (qa) {
      const n = Number(qa.dataset.count) || 0;
      if (!n) return;
      const yes = await confirmDialog({
        title: `Решить ${n} ${plural(n, "тест", "теста", "тестов")}?`,
        text: "Бот поставит все несданные тесты в очередь и решит их по одному. Результаты придут в Telegram.",
        ok: "В очередь",
      });
      if (!yes || !alive) return;
      busy(qa, true);
      try {
        const r = await api("api/quiz/force-all", { method: "POST" });
        busy(qa, false);
        toast(`Поставил ${r.count} тестов в очередь`);
        qa.disabled = true;
        qa.textContent = "В очереди";
      } catch (e) {
        busy(qa, false);
        toast(e.message, "error");
      }
      return;
    }
    const ao = ev.target.closest("[data-ask-open]");
    if (ao) {
      const d = closedList.find((x) => x.id === ao.dataset.askOpen);
      if (d) await askOpen(ao, [d]);
      return;
    }
    const ag = ev.target.closest("[data-ask-group]");
    if (ag) {
      await askOpen(ag, closedList);
      return;
    }
    if (ev.target.closest("[data-ask-close]")) {
      ev.target.closest(".ask-panel").remove();
      return;
    }
    const cp = ev.target.closest("[data-copy-letter]");
    if (cp) {
      await copyLetter(cp);
      return;
    }
    const qf = ev.target.closest("[data-quiz-force]");
    if (qf) {
      busy(qf, true);
      try {
        const r = await api("api/quiz/force", { method: "POST", body: { cmid: Number(qf.dataset.quizForce) } });
        toast(r.message || "Запускаю");
        busy(qf, false);
        qf.disabled = true;
        qf.textContent = "Запущено — результат придёт в Telegram";
      } catch (e) {
        busy(qf, false);
        toast(e.message, "error");
      }
      return;
    }
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

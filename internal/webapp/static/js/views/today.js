// «Сегодня»: что сейчас, что дальше, зайдёт ли бот. Обновляется раз в 10 секунд
// патчем по ключам — без дёрганья вёрстки и сброса прокрутки.
import { api } from "../api.js";
import { html, patch, setHTML } from "../dom.js";
import { icon } from "../icons.js";
import { dateLong, duration, now, syncClock, timeOf, weekdayShort, dateShort } from "../format.js";
import { app, empty, errorBlock, head, kindLabel, skeleton } from "../ui.js";
import { bindLessonActions, dayMode, joinShort, joinToggle, lessonCard, willJoin } from "../lesson.js";
import { toast } from "../toast.js";

function modes(isToday) {
  return [
    { id: "auto", label: "Как обычно", tone: "acc" },
    { id: "all", label: "Заходить на все", tone: "acc" },
    { id: "none", label: isToday ? "Не заходить сегодня" : "Не заходить в этот день", tone: "off" },
  ];
}

// modeSwitch — режим дня. При разных решениях по парам ни один сегмент не
// выбран, и это показано явно: рамка-пунктир и плашка «Свой выбор» с кнопкой сброса.
export function modeSwitch(mode, { compact = false, date = "", today = true, label = "Режим дня" } = {}) {
  const mixed = mode === "mixed";
  return html`<div class="segmented${compact ? " compact" : ""}${mixed ? " is-mixed" : ""}" role="radiogroup" aria-label="${label}" data-date="${date}">
    ${modes(today).map((m) => html`<button type="button" role="radio" aria-checked="${mode === m.id ? "true" : "false"}" data-mode="${m.id}" data-tone="${m.tone}">${m.label}</button>`)}
  </div>`;
}

export function modeHint(mode, today = true) {
  switch (mode) {
    case "auto": return html`<p class="daymode-hint"><strong>Как обычно:</strong> бот сам заходит на онлайн-пары, если вы не отказались.</p>`;
    case "all": return html`<p class="daymode-hint"><strong>На все пары:</strong> бот зайдёт на каждую онлайн-пару за 15 минут.</p>`;
    case "none": return html`<p class="daymode-hint"><strong>${today ? "Сегодня" : "В этот день"} без бота:</strong> он не зайдёт ни на одну пару.</p>`;
    case "mixed": return html`<div class="mixed-note" role="status">${icon("tools")}<span><strong>Свой выбор по парам.</strong> Решения разные — смотрите переключатели ниже.</span>
      <button type="button" class="btn btn-sm" data-mode="auto">Сбросить к обычному</button></div>`;
    default: return "";
  }
}

function botStatus(v, me) {
  if (!v.online) return { cls: "", ico: "place", title: "Очная пара", sub: "бот на неё не ходит" };
  if (!v.mine) return { cls: "", ico: "user", title: "Не ваша подгруппа", sub: "бот не заходит" };
  if (!me.active) return { cls: "bad", ico: "power", title: "Бот выключен", sub: "включите в «Ещё»" };
  if (v.decision === "no") return { cls: "", ico: "x", title: "Бот не заходит", sub: "вы отказались от пары" };
  const st = v.presence && v.presence.state;
  if (st === "room") return { cls: "ok", ico: "bot", title: "В комнате", sub: `бот здесь с ${timeOf(v.presence.updated_at)}` };
  if (st === "lobby") return { cls: "warn", ico: "bot", title: "Ждёт в лобби", sub: "модератор ещё не пустил" };
  if (st === "error") return { cls: "bad", ico: "warn", title: "Не зашёл", sub: v.presence.message || "ошибка захода" };
  if (!v.has_bbb) return { cls: "warn", ico: "nolink", title: "Нет ссылки", sub: "бот не знает, куда идти" };
  return { cls: "", ico: "bot", title: "Ещё не зашёл", sub: "зайдёт в ближайшие минуты" };
}

function recStatus(v) {
  if (v.recording) return { cls: "ok", dot: true, title: "Идёт запись", sub: "после пары будет конспект" };
  if (v.kind !== "lecture") return { cls: "", ico: "rec", title: "Без записи", sub: "пишутся только лекции" };
  if (v.presence && v.presence.state === "room") return { cls: "warn", ico: "rec", title: "Запись не идёт", sub: "конспекта может не быть" };
  return { cls: "", ico: "rec", title: "Запись не идёт", sub: "начнётся, когда бот зайдёт" };
}

function statusBox(s) {
  return html`<div class="status ${s.cls}">
    <strong>${s.dot ? html`<span class="rec-dot" aria-hidden="true"></span>` : icon(s.ico)}${s.title}</strong>
    <span>${s.sub}</span>
  </div>`;
}

function leftParts(ms) {
  const min = Math.max(0, Math.round(ms / 60000));
  if (min < 60) return { num: String(min), unit: "мин до конца" };
  const h = Math.floor(min / 60);
  const m = String(min % 60).padStart(2, "0");
  return { num: `${h}:${m}`, unit: "до конца пары" };
}

function nowCard(v, me, pending, next) {
  const begin = Date.parse(v.begin);
  const finish = Date.parse(v.finish);
  const t = now();
  const pct = Math.min(100, Math.max(0, ((t - begin) / (finish - begin)) * 100));
  const left = leftParts(finish - t);
  const off = v.online && v.mine && v.decision === "no";
  return html`<article class="now-card${off || !v.online ? " is-off" : ""}" aria-labelledby="now-title">
    <div class="now-top">
      <span class="live-tag"><span class="live-dot" aria-hidden="true"></span>Идёт сейчас</span>
      <span class="now-time mono">${v.start}–${v.end}</span>
    </div>
    <h2 class="now-title" id="now-title">${v.discipline}</h2>
    <div class="now-meta">
      <span>${icon(v.kind)}${kindLabel(v.kind, v.type)}</span>
      <span>${icon(v.online ? "online" : "place")}${v.online ? "онлайн" : v.place || "очно"}</span>
      ${v.teacher ? html`<span>${icon("user")}${v.teacher}</span>` : ""}
    </div>
    <div class="left-row"><div class="left-big"><span class="left-num">${left.num}</span><span class="left-unit">${left.unit}</span></div></div>
    <div class="progress" role="progressbar" aria-label="Прошло от пары" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(pct)}"><span style="width:${pct.toFixed(1)}%"></span></div>
    ${v.online ? html`<div class="status-grid">${statusBox(botStatus(v, me))}${statusBox(recStatus(v))}</div>` : html`<div class="status-grid">${statusBox(botStatus(v, me))}</div>`}
    ${v.online && v.mine
      ? html`<div class="now-actions${v.bbb_url ? " has-room" : ""}">
          ${joinToggle(v, me, { pending })}
          ${v.bbb_url ? html`<a class="btn room-btn" href="${v.bbb_url}" target="_blank" rel="noopener noreferrer" aria-label="Открыть комнату самому" title="Открыть комнату самому">${icon("ext")}<span>Комната</span></a>` : ""}
        </div>${linkGoto(v)}`
      : ""}
    ${next ? html`<p class="now-next">${icon("clock", "icon-sm")}<span>Дальше в <b class="mono">${next.start}</b> — ${next.discipline} · ${joinShort(next, me)}</span></p>` : ""}
  </article>`;
}

function linkGoto(v) {
  if (!v.online || !v.mine || v.has_bbb || v.phase === "past") return "";
  return html`<button type="button" class="btn btn-sm btn-warn" data-goto-link="${v.id}">${icon("link")}Вставить ссылку на комнату</button>`;
}

function nextCard(v, me, pending) {
  const inMs = Date.parse(v.begin) - now();
  const st = botStatus(v, me);
  return html`<article class="now-card is-calm" aria-labelledby="next-title">
    <div class="now-top">
      <span class="eyebrow-mini">Следующая пара</span>
      <span class="now-time mono">${v.start}–${v.end}</span>
    </div>
    <div class="left-big"><span class="left-unit">через</span><span class="left-num">${duration(inMs)}</span></div>
    <h2 class="now-title" id="next-title">${v.discipline}</h2>
    <div class="now-meta">
      <span>${icon(v.kind)}${kindLabel(v.kind, v.type)}</span>
      <span>${icon(v.online ? "online" : "place")}${v.online ? "онлайн" : v.place || "очно"}</span>
      ${v.teacher ? html`<span>${icon("user")}${v.teacher}</span>` : ""}
    </div>
    ${v.online && v.mine
      ? html`<div class="now-actions">${willJoin(v, me) && v.has_bbb ? "" : html`<div class="status-grid" style="grid-template-columns:1fr">${statusBox(st)}</div>`}${joinToggle(v, me, { pending })}${linkGoto(v)}</div>`
      : html`<div class="status-grid" style="grid-template-columns:1fr">${statusBox(st)}</div>`}
  </article>`;
}

function upcomingStrip(u, me, label) {
  if (!u) return "";
  const dayLabel = `${weekdayShort(u.date)}, ${dateShort(u.date)}`;
  return html`<div class="next-strip">
    <div class="when"><b>${u.start}</b><small>${label || dayLabel}</small></div>
    <div class="what"><b>${u.discipline}</b><small>${kindLabel(u.kind, u.type)} · ${joinShort(u, me)}</small></div>
    ${icon(willJoin(u, me) && u.has_bbb ? "bot" : u.online ? "x" : "place")}
  </div>`;
}

export function mountToday(root) {
  const st = { data: null, error: null, busy: 0, timer: 0, alive: true };
  setHTML(root, html`<div class="today-top" id="t-top"></div>
    <div class="today-grid"><div class="today-side" id="t-side"></div><div class="today-list" id="t-list"></div></div>`);
  const top = root.querySelector("#t-top");
  const side = root.querySelector("#t-side");
  const list = root.querySelector("#t-list");
  setHTML(side, skeleton("today"));

  const find = (id) => st.data && st.data.lessons.find((l) => l.id === id);
  const ctrl = bindLessonActions(root, {
    find,
    me: () => st.data.me,
    base: () => (st.data.mode === "all" ? "yes" : "auto"),
    render,
    merge(res) {
      const i = st.data.lessons.findIndex((l) => l.id === res.lesson.id);
      if (i >= 0) {
        const l = res.lesson;
        if (l.id === st.data.next_id && l.phase === "later") l.phase = "next";
        st.data.lessons[i] = l;
      }
      st.data.mode = res.mode;
      render();
    },
    busy(on) { st.busy += on ? 1 : -1; },
    reload: load,
  });

  // bg: фоновый опрос по таймеру — с низким приоритетом сети.
  async function load(bg = false) {
    try {
      const d = await api("api/day", bg ? { priority: "low" } : {});
      syncClock(d.now);
      if (!st.alive) return;
      app.offline = false;
      if (st.busy > 0) return; // идёт сохранение — не затираем оптимистичное состояние
      st.data = d;
      st.error = null;
    } catch (e) {
      if (e.status === 401) return;
      if (e.status === 0) app.offline = true;
      st.error = e;
    }
    render();
  }

  function render() {
    if (!st.alive) return;
    const d = st.data;
    if (!d) {
      patch(top, [{ key: "head", html: head({ title: "Сегодня" }) }]);
      if (st.error) {
        patch(side, [{ key: "err", html: errorBlock(st.error) }]);
      }
      return;
    }
    const me = d.me;
    const lessons = d.lessons;
    const pend = ctrl.pending;
    const topItems = [{ key: "head", html: head({ eyebrow: dateLong(d.date), title: "Сегодня" }) }];
    if (!me.active) {
      topItems.push({
        key: "banner",
        html: html`<div class="notice notice-coral banner-off" role="status">${icon("power")}<div class="notice-body"><strong>Бот за вас не заходит</strong><span>${me.reason}</span>
          <div class="btn-row">${!me.enabled && me.onboarded
            ? html`<button type="button" class="btn btn-primary btn-sm" data-action="enable">${icon("power")}Включить бота</button>`
            : html`<a class="btn btn-sm" href="#more/profile">Открыть профиль</a>`}</div></div></div>`,
      });
    }
    patch(top, topItems);

    const cur = lessons.find((l) => l.id === d.current_id);
    const next = lessons.find((l) => l.id === d.next_id);
    const sideItems = [];
    const mode = dayMode(lessons);
    if (mode !== "empty") {
      sideItems.push({
        key: "mode",
        html: html`<section class="daymode" aria-label="Режим дня" data-date="${d.date}">${modeSwitch(mode, { date: d.date })}${modeHint(mode)}</section>`,
      });
    }
    if (cur) {
      sideItems.push({ key: `now-${cur.id}`, html: nowCard(cur, me, pend.has(cur.id), next) });
    } else if (next) {
      sideItems.push({ key: `next-${next.id}`, html: nextCard(next, me, pend.has(next.id)) });
    } else if (lessons.length) {
      sideItems.push({
        key: "over",
        html: html`<section class="day-over"><span class="empty-ico">${icon("moon", "icon-lg")}</span><h2>На сегодня всё</h2>
          <p class="muted">Пары закончились. ${d.upcoming ? "Ближайшая — ниже." : "Дальше в расписании пусто."}</p>
          ${d.upcoming ? upcomingStrip(d.upcoming, me) : ""}</section>`,
      });
    } else {
      sideItems.push({
        key: "empty",
        html: html`${empty({
          ico: "calendar",
          img: app.illus.empty,
          title: "Сегодня пар нет",
          text: d.upcoming ? "Можно выдохнуть. Ближайшая пара:" : "В расписании на сегодня пусто.",
          action: d.upcoming ? upcomingStrip(d.upcoming, me) : "",
        })}`,
      });
    }
    patch(side, sideItems);

    const listItems = [];
    if (lessons.length) {
      const past = lessons.filter((l) => l.phase === "past");
      const rest = lessons.filter((l) => l.phase !== "past");
      listItems.push({ key: "title", html: html`<h2 class="section-title">Пары дня <span class="count">${lessons.length}</span></h2>` });
      if (past.length) {
        listItems.push({
          key: "past",
          html: html`<details class="fold past-fold"><summary>${icon("check")}Прошедшие · ${past.length}${icon("chev", "chev")}</summary>
            <div class="fold-body">${past.map((l) => lessonCard(l, me, { compact: true }))}</div></details>`,
        });
      }
      for (const l of rest) {
        listItems.push({ key: `l-${l.id}`, html: lessonCard(l, me, { controls: l.id !== (cur && cur.id), pending: pend.has(l.id) }) });
      }
    }
    patch(list, listItems);
  }

  root.addEventListener("click", async (ev) => {
    const b = ev.target.closest("[data-mode]");
    if (b && st.data) {
      if (st.modeBusy || b.getAttribute("aria-checked") === "true") return;
      st.modeBusy = true;
      const mode = b.dataset.mode;
      const snapshot = st.data.lessons.map((l) => ({ ...l }));
      const dec = mode === "all" ? "yes" : mode === "none" ? "no" : "auto";
      for (const l of st.data.lessons) {
        if (l.online && l.mine && l.phase !== "past") {
          l.decision = dec;
          l.will_join = willJoin(l, st.data.me);
        }
      }
      st.busy++;
      render();
      try {
        const d = await api("api/day", { method: "POST", body: { date: st.data.date, mode } });
        st.data = d;
        toast(mode === "none" ? "Сегодня бот не заходит" : mode === "all" ? "Бот зайдёт на все онлайн-пары" : "Режим дня: как обычно");
      } catch (e) {
        st.data.lessons = snapshot;
        toast(`Не сохранилось: ${e.message}`, "error");
      } finally {
        st.busy--;
        st.modeBusy = false;
        render();
      }
      return;
    }
    const a = ev.target.closest("[data-action]");
    if (!a) return;
    if (a.dataset.action === "retry") {
      setHTML(side, skeleton("today"));
      load();
    } else if (a.dataset.action === "enable") {
      a.disabled = true;
      try {
        await api("api/me", { method: "POST", body: { enabled: true } });
        toast("Бот снова заходит за вас");
        await load();
      } catch (e) {
        a.disabled = false;
        toast(e.message, "error");
      }
    }
  });

  const tick = () => {
    if (document.visibilityState === "visible") load(true);
  };
  const onVis = () => tick();
  document.addEventListener("visibilitychange", onVis);
  st.timer = setInterval(tick, 10000);
  load();

  return () => {
    st.alive = false;
    clearInterval(st.timer);
    document.removeEventListener("visibilitychange", onVis);
  };
}

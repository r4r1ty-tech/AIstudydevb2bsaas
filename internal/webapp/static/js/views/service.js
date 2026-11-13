// «Служебное»: всё, что умел прежний пульт — тестовый заход, аккаунты,
// люди, пары и ссылки, парсер, логи. Ручки API те же.
import { api } from "../api.js";
import { html, setHTML } from "../dom.js";
import { icon } from "../icons.js";
import { bytes, stamp } from "../format.js";
import { app, busy, errorBlock } from "../ui.js";
import { toast } from "../toast.js";

const TABS = [
  ["test", "Тест"],
  ["now", "Сейчас"],
  ["people", "Люди"],
  ["lessons", "Пары"],
  ["parser", "Парсер"],
  ["logs", "Логи"],
];

const PRESENCE = { none: "не в BBB", room: "в комнате", lobby: "в лобби", error: "ошибка" };

function testActive(j) {
  return !!(j && j.want && ["joining", "lobby", "room"].includes(j.status));
}

function testStatus(j) {
  switch (j.status) {
    case "room": return "в комнате";
    case "lobby": return "в лобби, ждёт модератора";
    case "joining": return "заходит…";
    case "error": return `ошибка${j.message ? `: ${j.message}` : ""}`;
    default: return "не в комнате";
  }
}

function lessonLine(l) {
  if (!l) return html`<p class="muted">нет</p>`;
  return html`<div class="svc-row">
    <b>${l.discipline}</b>
    <div class="row"><span class="mono">${l.date || ""} ${l.start}–${l.end}</span>
      <span class="chip ${l.online ? "chip-acc" : "chip-ghost"}">${l.online ? "онлайн" : "очно"}</span>
      ${l.type ? html`<span class="chip">${l.type}</span>` : ""}
      <span class="chip chip-ghost">${l.subgroup ? `подгр. ${l.subgroup}` : "вся группа"}</span></div>
    <span class="muted">${l.teacher}${l.place ? ` · ${l.place}` : ""}</span>
    ${l.bbb_url ? html`<span class="room-url">${l.bbb_url}</span>` : ""}
  </div>`;
}

export function mountService(root) {
  let tab = "test";
  let timer = 0;
  let alive = true;

  setHTML(root, html`<div class="service-tabs" role="tablist" aria-label="Служебные разделы">
      ${TABS.map(([id, label]) => html`<button type="button" role="tab" id="svc-t-${id}" aria-controls="svc-panel" aria-selected="${id === tab ? "true" : "false"}" data-svc="${id}">${label}</button>`)}
    </div>
    <div class="service-body" id="svc-panel" role="tabpanel" aria-labelledby="svc-t-${tab}"></div>`);
  const body = root.querySelector("#svc-panel");

  const loaders = {
    async test() {
      const j = await api("api/test");
      if (testActive(j)) {
        return html`<div class="svc-row">
          <p class="eyebrow-mini">Сейчас тест</p>
          <dl class="kv"><dt>Ссылка</dt><dd>${j.url || "—"}</dd><dt>Имя</dt><dd>${j.name || "тест"}</dd>
            <dt>Статус</dt><dd>${testStatus(j)}</dd><dt>Режим</dt><dd>${j.want === "listen" ? "со звуком" : "болванчик, без звука"}</dd></dl>
          <button type="button" class="btn btn-danger" data-svc-act="test-leave">${icon("logout")}Выйти</button>
        </div>`;
      }
      return html`<p class="field-hint">Отдельная тестовая комната: ссылка и имя живут только тут и на пары не попадают.</p>
        ${j.status === "error" ? html`<div class="notice notice-coral">${icon("warn")}<div class="notice-body">${testStatus(j)}</div></div>` : ""}
        <form class="form" id="svc-test-form">
          <label class="field"><span class="field-label">Ссылка BBB</span><input class="input" name="url" value="${j.url || ""}" placeholder="${app.settings.bbb_prefix || ""}…" autocomplete="off" spellcheck="false"></label>
          <label class="field"><span class="field-label">Имя в BBB</span><input class="input" name="name" value="${j.name || "тест"}" maxlength="64" autocomplete="off"></label>
          <div class="btn-row">
            <button type="submit" class="btn" data-want="dummy">Болванчик</button>
            <button type="submit" class="btn btn-primary" data-want="listen">Со звуком</button>
          </div>
        </form>`;
    },
    async now() {
      const d = await api("api/now");
      return html`<p class="eyebrow-mini">Аккаунты</p>
        ${d.accounts.length ? d.accounts.map((a) => html`<div class="svc-row">
          <div class="row"><span class="mono">${a.telegram_id}</span>${a.username ? html`<span>@${a.username}</span>` : ""}</div>
          <span>${a.fio || "нет ФИО"}</span>
          <div class="row">
            <span class="chip ${a.in_bot ? "chip-acc" : "chip-ghost"}">${a.in_bot ? "в боте" : "не в боте"}</span>
            ${a.has_profile ? "" : html`<span class="chip chip-amber">нет профиля</span>`}
            <span class="chip ${a.in_bbb === "room" ? "chip-acc" : a.in_bbb === "lobby" ? "chip-amber" : a.in_bbb === "error" ? "chip-coral" : "chip-ghost"}">BBB: ${PRESENCE[a.in_bbb] || a.in_bbb}</span>
          </div></div>`) : html`<p class="muted">пусто</p>`}
        <p class="eyebrow-mini">Текущая онлайн-пара</p>${lessonLine(d.current)}
        <p class="eyebrow-mini">Следующая</p>${lessonLine(d.next)}
        <p class="eyebrow-mini">Запись</p><p>${d.recording ? html`<span class="chip chip-coral"><span class="rec-dot"></span>пишется</span>` : html`<span class="chip chip-ghost">нет</span>`}</p>`;
    },
    async people() {
      const d = await api("api/people");
      if (!d.people.length) return html`<p class="muted">пусто</p>`;
      return html`${d.people.map((p) => {
        const u = p.user || { telegram_id: p.telegram_id };
        const id = p.telegram_id || u.telegram_id;
        const nick = u.username ? `@${u.username}` : [u.first_name, u.last_name].filter(Boolean).join(" ");
        const off = u.disabled_until && Date.parse(u.disabled_until) > Date.now();
        return html`<form class="svc-row form" data-person="${id}">
          <div class="row"><span class="mono">${id}</span><span>${nick}</span>
            <span class="chip ${p.in_whitelist ? "chip-acc" : "chip-ghost"}">${p.in_whitelist ? "в списке" : "не в списке"}</span>
            ${p.has_profile ? "" : html`<span class="chip chip-amber">нет профиля</span>`}
            ${off ? html`<span class="chip chip-amber">выкл. до ${stamp(u.disabled_until)}</span>` : ""}</div>
          <button type="button" class="join compact" role="switch" data-person-on aria-checked="${u.enabled ? "true" : "false"}" aria-label="Бот включён для ${u.fio || id}">
            <span class="join-track" aria-hidden="true"><span class="join-thumb">${icon(u.enabled ? "check" : "x")}</span></span>
            <span class="join-text"><span class="join-label">${u.enabled ? "Включён" : "Выключен"}</span></span>
          </button>
          <label class="field"><span class="field-label">ФИО</span><input class="input" name="fio" value="${u.fio || ""}"></label>
          <label class="field"><span class="field-label">Подгруппа</span><input class="input" name="sub" type="number" min="0" max="4" inputmode="numeric" value="${u.subgroup || 0}"></label>
          <label class="field"><span class="field-label">Вейкворды</span><input class="input" name="words" value="${(u.extra_words || []).join(", ")}" placeholder="лаба, зачёт"></label>
          <div class="btn-row">
            <button type="submit" class="btn btn-primary btn-sm">Сохранить</button>
            <button type="button" class="btn btn-sm" data-svc-act="off-today">Выключить на сегодня</button>
            <button type="button" class="btn btn-sm btn-ghost" data-svc-act="clear-off">Снять отключение</button>
          </div>
        </form>`;
      })}`;
    },
    async lessons() {
      const d = await api("api/lessons");
      return html`<p class="eyebrow-mini">Расписание · ${d.lessons.length}</p>
        ${d.lessons.length ? d.lessons.map(lessonLine) : html`<p class="muted">пар нет</p>`}
        <p class="eyebrow-mini">Ссылки BBB · ${d.bbb.length}</p>
        <form class="svc-row form" id="svc-bbb-form">
          <label class="field"><span class="field-label">Ключ (необязательно)</span><input class="input" name="key" autocomplete="off"></label>
          <label class="field"><span class="field-label">Дисциплина</span><input class="input" name="discipline" autocomplete="off"></label>
          <label class="field"><span class="field-label">Преподаватель</span><input class="input" name="teacher" autocomplete="off"></label>
          <label class="field"><span class="field-label">Ссылка</span><input class="input" name="url" placeholder="${app.settings.bbb_prefix || ""}…" required autocomplete="off"></label>
          <button type="submit" class="btn btn-primary">Сохранить ссылку</button>
        </form>
        ${d.bbb.map((b) => html`<div class="svc-row"><span class="mono" style="font-size:12.5px;overflow-wrap:anywhere">${b.key}</span><span class="room-url">${b.url || "— снята —"}</span></div>`)}
        <p class="eyebrow-mini">Записи · ${d.recordings.length}</p>
        ${d.recordings.length ? d.recordings.map((r) => html`<div class="svc-row"><span class="mono" style="overflow-wrap:anywhere">${r.name}</span>
          <div class="row"><span class="chip">${r.status || "—"}</span><span class="muted">${bytes(r.size)} · ${stamp(r.mod)}</span></div></div>`) : html`<p class="muted">записей нет</p>`}`;
    },
    async parser() {
      const run = await api("api/parser");
      return parserHTML(run);
    },
    async logs() {
      const d = await api("api/logs?limit=200");
      if (!d.events.length) return html`<p class="muted">событий нет</p>`;
      return html`${d.events.map((e) => html`<div class="ev t-${e.type}">
        <div class="ev-head"><span class="mono">${stamp(e.at)}</span><span class="ev-type">${e.type}</span>${e.telegram_id ? html`<span class="mono">${e.telegram_id}</span>` : ""}</div>
        <div class="ev-msg">${e.message}</div></div>`)}`;
    },
  };

  function parserHTML(run) {
    run = run || {};
    const never = !run.at || run.at.startsWith("0001-");
    return html`<div class="svc-row">
        <div class="row"><span class="chip ${run.ok ? "chip-acc" : "chip-coral"}">${run.ok ? "успешно" : never ? "ещё не было" : "сбой"}</span>
          <span>${never ? "" : stamp(run.at)}</span>${run.status ? html`<span class="chip chip-ghost">${run.status}</span>` : ""}</div>
        <span>пар: <b>${run.lesson_count || 0}</b>, онлайн: <b>${run.online_count || 0}</b></span>
      </div>
      <pre class="svc-pre">${run.diff || "(изменений нет)"}</pre>
      <button type="button" class="btn btn-primary" data-svc-act="refresh">${icon("refresh")}Обновить расписание сейчас</button>`;
  }

  async function show(id, quiet = false) {
    tab = id;
    root.querySelectorAll("[data-svc]").forEach((b) => b.setAttribute("aria-selected", b.dataset.svc === id ? "true" : "false"));
    body.setAttribute("aria-labelledby", `svc-t-${id}`);
    if (!quiet) setHTML(body, html`<div class="sk sk-block"></div>`);
    clearInterval(timer);
    try {
      const out = await loaders[id]();
      if (!alive || tab !== id) return;
      if (quiet && body.contains(document.activeElement) && document.activeElement.tagName === "INPUT") return;
      setHTML(body, out);
    } catch (e) {
      if (e.status === 401 || !alive) return;
      setHTML(body, errorBlock(e, "svc-retry"));
    }
    if (id === "test" || id === "now") {
      timer = setInterval(() => {
        if (document.visibilityState === "visible" && alive && tab === id) show(id, true);
      }, 4000);
    }
  }

  root.addEventListener("click", async (ev) => {
    const t = ev.target.closest("[data-svc]");
    if (t) { show(t.dataset.svc); return; }
    const want = ev.target.closest("[data-want]");
    if (want) want.form.dataset.want = want.dataset.want;
    if (ev.target.closest('[data-action="svc-retry"]')) { show(tab); return; }
    const a = ev.target.closest("[data-svc-act]");
    if (!a) return;
    const act = a.dataset.svcAct;
    try {
      if (act === "test-leave") {
        busy(a, true);
        await api("api/test", { method: "POST", body: { want: "leave" } });
        toast("Выходим из тестовой комнаты");
        show("test", true);
      } else if (act === "refresh") {
        busy(a, true);
        try {
          const run = await api("api/parser/refresh", { method: "POST" });
          setHTML(body, parserHTML(run));
          toast("Расписание обновлено");
        } catch (e) {
          if (e.body && e.body.at !== undefined) setHTML(body, parserHTML(e.body));
          throw e;
        }
      } else if (act === "off-today" || act === "clear-off") {
        const id = a.closest("[data-person]").dataset.person;
        busy(a, true);
        await api(`api/people/${id}`, { method: "POST", body: { disable_today: act === "off-today" } });
        toast(act === "off-today" ? "Выключен до конца дня" : "Отключение снято");
        show("people", true);
      }
    } catch (e) {
      busy(a, false);
      toast(e.message, "error");
    }
  });

  root.addEventListener("click", async (ev) => {
    const sw = ev.target.closest("[data-person-on]");
    if (!sw || sw.getAttribute("aria-busy") === "true") return;
    const f = sw.closest("[data-person]");
    const on = sw.getAttribute("aria-checked") !== "true";
    sw.setAttribute("aria-busy", "true");
    try {
      await api(`api/people/${f.dataset.person}`, { method: "POST", body: { enabled: on } });
      toast(on ? "Включён" : "Выключен");
      show("people", true);
    } catch (e) {
      toast(e.message, "error");
    } finally {
      sw.removeAttribute("aria-busy");
    }
  });

  root.addEventListener("submit", async (ev) => {
    const f = ev.target;
    ev.preventDefault();
    ev.stopPropagation();
    const btn = ev.submitter || f.querySelector('[type="submit"]');
    try {
      busy(btn, true);
      if (f.id === "svc-test-form") {
        const url = f.url.value.trim();
        if (!url) throw new Error("Нужна ссылка");
        const wantVal = (ev.submitter && ev.submitter.dataset.want) || f.dataset.want || "dummy";
        await api("api/test", { method: "POST", body: { url, want: wantVal, name: f.name.value.trim() || "тест" } });
        toast(wantVal === "listen" ? "Заход со звуком" : "Болванчик заходит");
        show("test", true);
      } else if (f.id === "svc-bbb-form") {
        await api("api/bbb", { method: "POST", body: {
          key: f.key.value.trim(), discipline: f.discipline.value.trim(), teacher: f.teacher.value.trim(), url: f.url.value.trim(),
        } });
        toast("Ссылка сохранена");
        show("lessons", true);
      } else if (f.dataset.person) {
        await api(`api/people/${f.dataset.person}`, { method: "POST", body: {
          fio: f.fio.value, subgroup: Number(f.sub.value || 0), extra_words: f.words.value,
        } });
        toast("Сохранено");
        show("people", true);
      }
    } catch (e) {
      toast(e.message, "error");
    } finally {
      busy(btn, false);
    }
  });

  show(tab);
  return () => {
    alive = false;
    clearInterval(timer);
  };
}

// «Ещё»: аккаунт универа, профиль, комнаты BBB и свёрнутое «Служебное».
import { api } from "../api.js";
import { html, setHTML, str } from "../dom.js";
import { icon } from "../icons.js";
import { ago, syncClock } from "../format.js";
import { app, busy, errorBlock, head, kindLabel, skeleton } from "../ui.js";
import { toast } from "../toast.js";
import { mountService } from "./service.js";
import { aiFormHTML, bindAI } from "../ai.js";

function section(id, ico, title, sub, body) {
  return html`<section class="card more-section" id="sec-${id}" aria-labelledby="h-${id}">
    <header><span class="sec-ico">${icon(ico)}</span><div><h2 id="h-${id}">${title}</h2>${sub ? html`<p>${sub}</p>` : ""}</div></header>
    ${body}
  </section>`;
}

function accountBody(m) {
  if (m.connected) {
    return html`<div class="conn-state ok">${icon("check")}<span>Подключён как <b>${m.login}</b>${m.synced_at ? html` · обновлено ${ago(m.synced_at)}` : ""}</span></div>
      ${m.message ? html`<p class="field-hint">${m.message}</p>` : ""}
      <button type="button" class="btn btn-ghost" data-action="moodle-logout">${icon("logout")}Отключить</button>`;
  }
  return html`<div class="conn-state">${icon("key")}<span>Не подключён</span></div>
    <form class="form" id="moodle-form" novalidate>
      <label class="field"><span class="field-label">Логин</span>
        <input class="input" name="login" autocomplete="username" autocapitalize="none" spellcheck="false" required></label>
      <label class="field"><span class="field-label">Пароль</span>
        <input class="input" name="password" type="password" autocomplete="current-password" required></label>
      <div id="moodle-msg" role="alert"></div>
      <button type="submit" class="btn btn-primary">${icon("key")}Подключить</button>
      <p class="field-hint">Пароль хранится на сервере зашифрованным: по нему бот сам обновляет задания Moodle и, если логин тот же, расписание из ЛК. «Отключить» стирает его.</p>
    </form>`;
}

function profileBody(me) {
  const sub = me.subgroup || 0;
  return html`<form class="form" id="profile-form" novalidate>
    <div class="conn-state${me.active ? " ok" : ""}">${icon(me.active ? "check" : "power")}<span>${me.active ? "Бот заходит за вас на онлайн-пары." : me.reason}</span></div>
    <label class="field"><span class="field-label">Имя для BBB</span>
      <input class="input" name="fio" value="${me.fio || ""}" autocomplete="name" maxlength="64" placeholder="Фамилия Имя Отчество">
      <span class="field-hint">Так бот подписывается в комнате.</span></label>
    <div class="field"><span class="field-label" id="sub-l">Подгруппа</span>
      <div class="segmented" role="radiogroup" aria-labelledby="sub-l">
        ${[1, 2].map((n) => html`<button type="button" role="radio" data-sub="${n}" data-tone="acc" aria-checked="${sub === n ? "true" : "false"}">${n}-я</button>`)}
      </div>
      ${sub > 2 ? html`<span class="field-hint">Сейчас указана ${sub}-я.</span>` : ""}
    </div>
    <label class="field"><span class="field-label">Слова-триггеры</span>
      <input class="input" name="words" value="${(me.extra_words || []).join(", ")}" placeholder="лаба, дедлайн" autocomplete="off">
      <span class="field-hint">Услышит такое слово на паре — напишет вам в Telegram. Через запятую.</span></label>
    <div class="field"><span class="field-label">Бот уже ловит</span>
      <div class="words">${(me.wake_words || []).map((w) => html`<span class="chip chip-ghost">${w}</span>`)}</div></div>
    <button type="button" class="join" role="switch" data-enabled aria-checked="${me.enabled ? "true" : "false"}" aria-label="Бот заходит за меня">
      <span class="join-track" aria-hidden="true"><span class="join-thumb">${icon(me.enabled ? "check" : "x")}</span></span>
      <span class="join-text"><span class="join-label">${me.enabled ? "Бот заходит за меня" : "Бот выключен"}</span>
        <span class="join-hint">${me.enabled ? "выключите, чтобы он никуда не ходил" : "включите, чтобы он снова заходил"}</span></span>
    </button>
    <button type="submit" class="btn btn-primary">Сохранить профиль</button>
  </form>`;
}

function roomsBody(rooms) {
  if (!rooms.length) return html`<p class="muted">Онлайн-пар в расписании нет — и комнат тоже.</p>`;
  const prefix = app.settings.bbb_prefix || "";
  return html`<div>${rooms.map((r, i) => html`<div class="room">
    <p class="room-title">${r.discipline}</p>
    <div class="row"><span class="chip">${icon(r.kind)}${kindLabel(r.kind, r.type)}</span><span class="muted" style="font-size:14px">${r.teacher}</span></div>
    <div class="room-row">
      ${r.url ? html`<span class="room-url">${r.url}</span>` : html`<span class="chip chip-amber">${icon("nolink")}нет ссылки</span>`}
      <button type="button" class="btn btn-sm${r.url ? " btn-ghost" : ""}" data-edit-room="${i}" aria-expanded="false" aria-controls="room-f-${i}">${r.url ? "Изменить" : "Добавить ссылку"}</button>
    </div>
    <form class="inline-form" id="room-f-${i}" data-room="${r.key}" hidden>
      <label class="visually-hidden" for="room-i-${i}">Ссылка на комнату</label>
      <input class="input" id="room-i-${i}" name="url" type="url" inputmode="url" value="${r.url}" placeholder="${prefix}…" autocomplete="off" spellcheck="false">
      <button type="submit" class="btn btn-primary">Сохранить</button>
    </form>
  </div>`)}</div>`;
}

export function mountMore(root, ctx) {
  let alive = true;
  let stopService = null;
  const st = { me: null, moodle: null, rooms: [], serviceOpen: false, ai: null };
  setHTML(root, skeleton());

  async function load() {
    try {
      const [me, dl, rooms, ai] = await Promise.all([api("api/me"), api("api/deadlines"), api("api/rooms"),
        api("api/llm").catch(() => null)]);
      if (!alive) return;
      st.ai = ai;
      st.me = me;
      st.moodle = dl.moodle;
      syncClock(dl.now);
      st.rooms = rooms.rooms;
    } catch (e) {
      if (e.status === 401 || !alive) return;
      setHTML(root, html`${head({ title: "Ещё" })}${errorBlock(e)}`);
      return;
    }
    render();
    if (ctx.sub) {
      const el = root.querySelector(`#sec-${ctx.sub}`) || (ctx.sub === "service" && root.querySelector("#service"));
      if (el) {
        if (el.tagName === "DETAILS") { el.open = true; st.serviceOpen = true; }
        el.scrollIntoView({ block: "start" });
        const first = el.querySelector("input, button");
        if (first && ctx.sub === "account") first.focus({ preventScroll: true });
      }
    }
  }

  function render() {
    if (stopService) stopService();
    setHTML(root, html`${head({ eyebrow: "Настройки и служебное", title: "Ещё" })}
      ${section("account", "key", "Аккаунт универа", "Moodle: тесты и задания со сроками; ЛК: расписание, подгруппы и ссылки BBB", html`<div id="acc-body" class="stack-lg">${accountBody(st.moodle)}</div>`)}
      ${section("profile", "user", "Профиль", "Как бот заходит за вас", profileBody(st.me))}
      ${st.ai ? section("ai", "bot", "ИИ для тестов", "Какой моделью и чьим ключом бот решает твои тесты в Moodle", aiFormHTML(st.ai)) : ""}
      ${section("rooms", "door", "Комнаты BBB", "Ссылки по предметам — подставляются на каждую пару", roomsBody(st.rooms))}
      ${app.settings?.is_admin ? html`<details class="card fold service" id="service" ${st.serviceOpen ? "open" : ""}>
        <summary>${icon("tools")}Служебное${icon("chev", "chev")}</summary>
        <div id="service-root"></div>
      </details>` : ""}`);
    const det = root.querySelector("#service");
    stopService = null;
    if (!det) return;
    det.addEventListener("toggle", () => {
      st.serviceOpen = det.open;
      if (det.open && !stopService) stopService = mountService(root.querySelector("#service-root"));
    });
    if (det.open) stopService = mountService(root.querySelector("#service-root"));
  }

  root.addEventListener("click", async (ev) => {
    const sw = ev.target.closest("[data-enabled]");
    if (sw) {
      const on = sw.getAttribute("aria-checked") !== "true";
      sw.setAttribute("aria-checked", String(on));
      sw.querySelector(".join-thumb").innerHTML = str(icon(on ? "check" : "x"));
      sw.querySelector(".join-label").textContent = on ? "Бот заходит за меня" : "Бот выключен";
      sw.querySelector(".join-hint").textContent = on ? "сохраните профиль, чтобы включить" : "сохраните профиль, чтобы выключить";
      return;
    }
    const subBtn = ev.target.closest("[data-sub]");
    if (subBtn) {
      subBtn.parentElement.querySelectorAll("[data-sub]").forEach((b) => b.setAttribute("aria-checked", b === subBtn ? "true" : "false"));
      return;
    }
    const edit = ev.target.closest("[data-edit-room]");
    if (edit) {
      const f = root.querySelector(`#room-f-${edit.dataset.editRoom}`);
      f.hidden = !f.hidden;
      edit.setAttribute("aria-expanded", String(!f.hidden));
      if (!f.hidden) f.querySelector("input").focus();
      return;
    }
    const act = ev.target.closest("[data-action]");
    if (!act) return;
    if (act.dataset.action === "retry") {
      setHTML(root, skeleton());
      load();
    } else if (act.dataset.action === "moodle-logout") {
      busy(act, true);
      try {
        st.moodle = await api("api/moodle/logout", { method: "POST" });
        root.querySelector("#acc-body").innerHTML = "";
        render();
        ctx.refreshBadge();
        toast("Moodle отключён");
      } catch (e) {
        busy(act, false);
        toast(e.message, "error");
      }
    }
  });

  root.addEventListener("submit", async (ev) => {
    const form = ev.target;
    if (form.id === "moodle-form") {
      ev.preventDefault();
      const msg = form.querySelector("#moodle-msg");
      const login = form.login.value.trim();
      const password = form.password.value;
      if (!login || !password) {
        setHTML(msg, html`<p class="form-error">Введите логин и пароль.</p>`);
        return;
      }
      const btn = form.querySelector('[type="submit"]');
      busy(btn, true);
      setHTML(msg, html``);
      try {
        st.moodle = await api("api/moodle/login", { method: "POST", body: { login, password } });
        form.password.value = "";
        toast("Moodle подключён");
        ctx.refreshBadge();
        render();
      } catch (e) {
        form.password.value = "";
        const soft = e.status === 501;
        setHTML(msg, html`<div class="notice ${soft ? "notice-amber" : "notice-coral"}">${icon(soft ? "clock" : "warn")}<div class="notice-body">${e.message}</div></div>`);
        busy(btn, false);
      }
      return;
    }
    if (form.id === "profile-form") {
      ev.preventDefault();
      const btn = form.querySelector('[type="submit"]');
      const subSel = form.querySelector('[data-sub][aria-checked="true"]');
      const sw = form.querySelector("[data-enabled]");
      const body = { fio: form.fio.value, extra_words: form.words.value, enabled: sw.getAttribute("aria-checked") === "true" };
      if (subSel) body.subgroup = Number(subSel.dataset.sub);
      busy(btn, true);
      try {
        st.me = await api("api/me", { method: "POST", body });
        toast("Профиль сохранён");
        render();
        root.querySelector("#sec-profile").scrollIntoView({ block: "nearest" });
      } catch (e) {
        toast(e.message, "error");
        busy(btn, false);
      }
      return;
    }
    if (form.dataset.room) {
      ev.preventDefault();
      const url = form.url.value.trim();
      const prefix = app.settings.bbb_prefix || "";
      if (!url || (prefix && !url.startsWith(prefix))) {
        toast(prefix ? `Нужна ссылка вида ${prefix}…` : "Вставьте ссылку на комнату", "error");
        return;
      }
      const btn = form.querySelector('[type="submit"]');
      busy(btn, true);
      try {
        await api("api/bbb", { method: "POST", body: { key: form.dataset.room, url } });
        const r = await api("api/rooms");
        st.rooms = r.rooms;
        toast("Ссылка сохранена");
        render();
        root.querySelector("#sec-rooms").scrollIntoView({ block: "nearest" });
      } catch (e) {
        toast(e.message, "error");
        busy(btn, false);
      }
    }
  });

  bindAI(root, () => st.ai, (v) => { st.ai = v; }, () => {
    render();
    const el = root.querySelector("#sec-ai");
    if (el) el.scrollIntoView({ block: "nearest" });
  });

  load();
  return () => {
    alive = false;
    if (stopService) stopService();
  };
}

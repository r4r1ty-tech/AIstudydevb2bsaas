// «ИИ для тестов»: свой провайдер, модель и ключ. Общий для «Ещё» (каждый
// себе) и «Служебного» (админ видит ещё и выбор всех).
import { api } from "./api.js";
import { html, setHTML } from "./dom.js";
import { stamp } from "./format.js";
import { icon } from "./icons.js";
import { busy, errorBlock } from "./ui.js";
import { toast } from "./toast.js";

let activeKeys = null;

function createKeysState(root) {
  return {
    root,
    loading: false,
    loaded: false,
    error: null,
    keys: [],
    vaultReady: true,
    limits: new Map(),
    shareDrafts: {},
    errors: {},
  };
}

function fmtNum(n) {
  return Number(n || 0).toLocaleString("ru-RU");
}

function formatCooldown(sec) {
  sec = Math.max(0, Math.round(Number(sec) || 0));
  if (sec < 60) return `${sec} сек.`;
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  if (m < 60) return s ? `${m} мин ${s} сек.` : `${m} мин`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return mm ? `${h} ч ${mm} мин` : `${h} ч`;
}

function normalizeLimits(res) {
  const list = Array.isArray(res) ? res : (res && res.providers) || [];
  const map = new Map();
  for (const item of list) {
    if (item && item.provider) map.set(item.provider, item);
  }
  return map;
}

function keyCardHTML(k, lim, state) {
  const cdSec = Math.max(Number(k.cooldown_sec || 0), Number((lim && lim.cooldown_sec) || 0));
  const shareChecked = k.configured ? Boolean(k.share_pool) : Boolean(state.shareDrafts[k.provider]);
  const errMsg = state.errors[k.provider] || "";
  const disabled = !state.vaultReady;

  return html`<div class="svc-row ai-key-card" data-ai-provider="${k.provider}">
    <div class="row ai-key-head">
      <b>${k.name || k.provider}</b>
      ${k.configured
        ? html`<span class="chip chip-acc">${icon("check")}подключён</span>`
        : html`<span class="chip chip-ghost">не задан</span>`}
    </div>
    ${k.configured
      ? html`<div class="row">
          ${k.mask ? html`<span class="chip chip-ghost mono">${k.mask}</span>` : ""}
          ${k.updated_at ? html`<span class="muted">обновлён ${stamp(k.updated_at)}</span>` : ""}
        </div>`
      : ""}
    ${cdSec > 0
      ? html`<div class="notice notice-amber" role="status">${icon("clock")}<div class="notice-body">
          <strong>Ключ на паузе (cooldown)</strong>
          <span>Осталось примерно ${formatCooldown(cdSec)} — пока действует пауза, бот пропускает этот ключ.</span>
        </div></div>`
      : ""}
    ${lim
      ? html`<div class="ai-limits">
          <div class="row">
            <span>Запросы${lim.day ? ` (${lim.day})` : ""}: <b>${fmtNum(lim.requests)}</b></span>
            <span class="muted">·</span>
            <span>остаток ~<b>${fmtNum(lim.remaining_estimate)}</b> из ~${fmtNum(lim.estimated_daily_requests)}</span>
          </div>
          <div class="row muted">
            <span>Токены: вход <b>${fmtNum(lim.input_tokens)}</b> · выход <b>${fmtNum(lim.output_tokens)}</b></span>
          </div>
          <p class="field-hint">${lim.estimated_note ? `${lim.estimated_note} — ` : ""}ориентировочный подсчёт по нашему расходу.</p>
        </div>`
      : ""}
    <label class="toggle-line">
      <span>Делиться ключом с пулом конспектов</span>
      <input class="check" type="checkbox" data-ai-share="${k.provider}" ${shareChecked ? "checked" : ""} ${disabled ? "disabled" : ""}>
    </label>
    <form class="form" data-ai-key-form="${k.provider}">
      <label class="field">
        <span class="field-label">${k.configured ? "Заменить ключ" : "Добавить ключ"}</span>
        <input class="input" type="password" name="key" autocomplete="off" spellcheck="false" placeholder="${k.configured ? "Введите новый API-ключ для замены" : "Вставьте API-ключ"}" ${disabled ? "disabled" : ""}>
      </label>
      <div data-ai-key-err="${k.provider}">${errMsg ? html`<div class="notice notice-coral" role="alert">${icon("warn")}<div class="notice-body">${errMsg}</div></div>` : ""}</div>
      <div class="btn-row">
        <button type="submit" class="btn btn-primary btn-sm" ${disabled ? "disabled" : ""}>${k.configured ? "Заменить" : "Добавить"}</button>
        ${k.configured ? html`<button type="button" class="btn btn-danger btn-sm" data-ai-key-del="${k.provider}">Удалить</button>` : ""}
      </div>
    </form>
  </div>`;
}

function keysSectionHTML(state) {
  const title = html`<h3 class="card-title">Мои ключи ИИ</h3>`;
  if (!state || (!state.loaded && !state.error)) {
    return html`${title}<p class="muted">Загружаю личные ключи и лимиты…</p>`;
  }
  if (state.error) {
    return html`${title}${errorBlock(state.error, "ai-keys-retry")}`;
  }
  return html`${title}
    <p class="field-hint">Личные ключи шифруются на сервере и используются в вашей цепочке решения тестов.</p>
    ${!state.vaultReady
      ? html`<div class="notice notice-amber" role="status">${icon("warn")}<div class="notice-body">
          <strong>Сохранение ключей недоступно</strong>
          <span>На сервере не настроено шифрование (vault) — добавлять и менять личные ключи сейчас нельзя.</span>
        </div></div>`
      : ""}
    ${state.keys.length
      ? state.keys.map((k) => keyCardHTML(k, state.limits.get(k.provider) || null, state))
      : html`<p class="muted">Провайдеры личных ключей не найдены.</p>`}`;
}

export function secs(ms) { return ms >= 1000 ? `${(ms / 1000).toFixed(1)} с` : `${ms} мс`; }

export function keyLabel(k) { return `${k.label}${k.tail ? ` ${k.tail}` : ""}`; }

export function attemptsHTML(list) {
  if (!list || !list.length) return html`<p class="muted">вызовов не было</p>`;
  return html`${list.map((a) => html`<div class="row"><span class="chip ${a.error ? "chip-coral" : "chip-acc"}">${a.error ? "мимо" : "ответ"}</span>
    <span class="mono">${a.provider}/${a.model}</span><span class="chip chip-ghost">ключ: ${a.key_label || `#${a.key}`}</span><span class="muted">${secs(a.took_ms)}</span></div>
    ${a.error ? html`<span class="muted" style="overflow-wrap:anywhere">${a.error}</span>` : ""}`)}`;
}

function chainHTML(list, n = 4) {
  if (!list || !list.length) return html`<span class="muted">— нет ключей —</span>`;
  const head = list.slice(0, n);
  return html`<span class="mono" style="overflow-wrap:anywhere">${head.join(" → ")}${list.length > n ? ` → … ещё ${list.length - n}` : ""}</span>`;
}

function optionsFor(st, provider) {
  const p = st.providers.find((x) => x.id === provider);
  return { models: p ? p.models : [], keys: p ? p.keys : [] };
}

export function aiFormHTML(st) {
  const ch = st.choice || {};
  const { models, keys } = optionsFor(st, ch.provider);
  return html`<div class="stack-lg">
    <p class="field-hint">${st.me ? html`Ты: <b>${st.me}</b>. ` : ""}Если твой ключ или модель не ответили — бот пробует остальные ключи этой модели, потом общую цепочку.</p>
    <div class="svc-row"><p class="eyebrow-mini">Твои тесты решает по порядку</p>${chainHTML(st.chain)}</div>
    <form class="form" id="ai-form">
      <label class="field"><span class="field-label">Провайдер</span>
        <select class="input" name="provider">
          <option value="" ${!ch.provider ? "selected" : ""}>Общая цепочка (как у всех)</option>
          ${st.providers.map((p) => html`<option value="${p.id}" ${p.id === ch.provider ? "selected" : ""}>${p.name}</option>`)}
        </select></label>
      <label class="field"><span class="field-label">Модель</span>
        <input class="input" name="model" list="ai-models" value="${ch.model || ""}" placeholder="${models[0] || "по умолчанию у провайдера"}" autocomplete="off" spellcheck="false">
        <datalist id="ai-models">${models.map((m) => html`<option value="${m}">`)}</datalist></label>
      <label class="field"><span class="field-label">Ключ</span>
        <select class="input" name="key">
          <option value="0" ${!ch.key ? "selected" : ""}>Любой (по очереди)</option>
          ${keys.map((k) => html`<option value="${k.n}" ${k.n === ch.key ? "selected" : ""}>${keyLabel(k)}</option>`)}
        </select></label>
      <div class="btn-row">
        <button type="submit" class="btn btn-primary">Сохранить</button>
        <button type="button" class="btn" data-ai="test">Проверить</button>
        <button type="button" class="btn btn-ghost" data-ai="models">Все модели</button>
      </div>
      <div id="ai-out"></div>
    </form>
    <section class="stack" id="ai-keys-root" aria-label="Мои ключи ИИ">${keysSectionHTML(activeKeys)}</section>
  </div>`;
}

function setKeyErr(root, provider, msg) {
  const box = root.querySelector(`[data-ai-key-err="${CSS.escape(provider)}"]`);
  if (!box) return;
  setHTML(box, msg ? html`<div class="notice notice-coral" role="alert">${icon("warn")}<div class="notice-body">${msg}</div></div>` : html``);
}

// bindAI вешает обработчики на root; get/set — текущее состояние, rerender — перерисовать.
export function bindAI(root, get, set, rerender) {
  const ks = createKeysState(root);
  activeKeys = ks;

  function applyKeysAndLimits(keysRes, limitsRes) {
    ks.keys = (keysRes && keysRes.keys) || [];
    ks.vaultReady = Boolean(keysRes && keysRes.vault_ready);
    ks.limits = normalizeLimits(limitsRes);
    ks.loaded = true;
    ks.loading = false;
    ks.error = null;
  }

  function renderKeysOnly() {
    if (!root.isConnected || activeKeys !== ks) return;
    const box = root.querySelector("#ai-keys-root");
    if (box) setHTML(box, keysSectionHTML(ks));
  }

  async function loadKeys() {
    if (ks.loading) return;
    ks.loading = true;
    ks.error = null;
    renderKeysOnly();
    try {
      const [keysRes, limitsRes] = await Promise.all([api("api/llm/keys"), api("api/llm/limits")]);
      if (activeKeys !== ks) return;
      applyKeysAndLimits(keysRes, limitsRes);
    } catch (e) {
      if (e.status === 401 || activeKeys !== ks) return;
      ks.loading = false;
      ks.error = e;
    }
    renderKeysOnly();
  }

  async function reloadAll() {
    const [llmRes, keysRes, limitsRes] = await Promise.all([
      api("api/llm"),
      api("api/llm/keys"),
      api("api/llm/limits"),
    ]);
    if (!root.isConnected || activeKeys !== ks) return;
    applyKeysAndLimits(keysRes, limitsRes);
    set(llmRes);
    rerender();
  }

  const mo = new MutationObserver(() => {
    if (!root.isConnected || activeKeys !== ks) {
      mo.disconnect();
      return;
    }
    if (root.querySelector("#ai-keys-root") && !ks.loaded && !ks.loading && !ks.error) {
      loadKeys();
    }
  });
  mo.observe(root, { childList: true });
  if (root.querySelector("#ai-keys-root") && !ks.loaded && !ks.loading) {
    loadKeys();
  }

  root.addEventListener("change", async (ev) => {
    const shareCb = ev.target.closest("[data-ai-share]");
    if (shareCb) {
      const provider = shareCb.dataset.aiShare;
      const k = ks.keys.find((x) => x.provider === provider);
      if (!k || !k.configured) {
        ks.shareDrafts[provider] = shareCb.checked;
        return;
      }
      const nextVal = shareCb.checked;
      ks.errors[provider] = "";
      setKeyErr(root, provider, "");
      shareCb.disabled = true;
      try {
        await api(`api/llm/keys/${encodeURIComponent(provider)}`, {
          method: "PUT",
          body: { key: "", share_pool: nextVal },
        });
        toast(nextVal ? "Ключ доступен пулу конспектов" : "Ключ исключён из пула конспектов");
        await reloadAll();
      } catch (e) {
        shareCb.checked = !nextVal;
        shareCb.disabled = !ks.vaultReady;
        ks.errors[provider] = e.message;
        setKeyErr(root, provider, e.message);
        toast(e.message, "error");
      }
      return;
    }

    const f = ev.target.form;
    if (!f || f.id !== "ai-form" || ev.target.name !== "provider") return;
    const { models, keys } = optionsFor(get(), ev.target.value);
    f.model.value = "";
    f.model.placeholder = models[0] || "по умолчанию у провайдера";
    setHTML(f.querySelector("#ai-models"), html`${models.map((m) => html`<option value="${m}">`)}`);
    setHTML(f.key, html`<option value="0" selected>Любой (по очереди)</option>${keys.map((k) => html`<option value="${k.n}">${keyLabel(k)}</option>`)}`);
  });

  root.addEventListener("submit", async (ev) => {
    const keyForm = ev.target.closest("[data-ai-key-form]");
    if (keyForm) {
      ev.preventDefault();
      ev.stopPropagation();
      const provider = keyForm.dataset.aiKeyForm;
      const input = keyForm.elements.key;
      const keyVal = (input ? input.value : "").trim();
      if (!keyVal) {
        const msg = "Введите API-ключ.";
        ks.errors[provider] = msg;
        setKeyErr(root, provider, msg);
        if (input) input.focus();
        return;
      }
      const card = keyForm.closest(".ai-key-card");
      const shareCb = card && card.querySelector("[data-ai-share]");
      const sharePool = shareCb ? shareCb.checked : Boolean(ks.shareDrafts[provider]);
      const btn = keyForm.querySelector('[type="submit"]');
      const delBtn = keyForm.querySelector("[data-ai-key-del]");
      const origText = btn ? btn.textContent : "";
      ks.errors[provider] = "";
      setKeyErr(root, provider, "");
      if (btn) {
        btn.disabled = true;
        btn.setAttribute("aria-busy", "true");
        btn.textContent = "Проверяю ключ...";
      }
      if (input) input.disabled = true;
      if (shareCb) shareCb.disabled = true;
      if (delBtn) delBtn.disabled = true;
      try {
        await api(`api/llm/keys/${encodeURIComponent(provider)}`, {
          method: "PUT",
          body: { key: keyVal, share_pool: sharePool },
        });
        if (input) input.value = "";
        delete ks.shareDrafts[provider];
        toast("Ключ проверен и сохранён");
        await reloadAll();
      } catch (e) {
        ks.errors[provider] = e.message;
        setKeyErr(root, provider, e.message);
        toast(e.message, "error");
        if (btn) {
          btn.disabled = !ks.vaultReady;
          btn.removeAttribute("aria-busy");
          btn.textContent = origText;
        }
        if (input) input.disabled = !ks.vaultReady;
        if (shareCb) shareCb.disabled = !ks.vaultReady;
        if (delBtn) delBtn.disabled = false;
      }
      return;
    }

    const f = ev.target;
    if (f.id !== "ai-form") return;
    ev.preventDefault();
    ev.stopPropagation();
    const btn = f.querySelector('[type="submit"]');
    busy(btn, true);
    try {
      set(await api("api/llm/choice", { method: "POST", body: { provider: f.provider.value, model: f.model.value.trim(), key: Number(f.key.value) } }));
      toast(f.provider.value ? "Сохранено: твои тесты решает выбранная модель" : "Вернул общую цепочку");
      rerender();
    } catch (e) {
      toast(e.message, "error");
      busy(btn, false);
    }
  });

  root.addEventListener("click", async (ev) => {
    if (ev.target.closest('[data-action="ai-keys-retry"]')) {
      loadKeys();
      return;
    }
    const delBtn = ev.target.closest("[data-ai-key-del]");
    if (delBtn) {
      const provider = delBtn.dataset.aiKeyDel;
      ks.errors[provider] = "";
      setKeyErr(root, provider, "");
      busy(delBtn, true);
      try {
        await api(`api/llm/keys/${encodeURIComponent(provider)}`, { method: "DELETE" });
        toast("Ключ удалён");
        await reloadAll();
      } catch (e) {
        busy(delBtn, false);
        ks.errors[provider] = e.message;
        setKeyErr(root, provider, e.message);
        toast(e.message, "error");
      }
      return;
    }

    const a = ev.target.closest("[data-ai]");
    if (!a) return;
    const f = a.form;
    const out = f.querySelector("#ai-out");
    const st = get();
    const provider = f.provider.value || (st.providers[0] && st.providers[0].id) || "";
    if (!provider) { toast("Нет ни одного провайдера", "error"); return; }
    busy(a, true);
    try {
      if (a.dataset.ai === "test") {
        setHTML(out, html`<p class="muted">Спрашиваю модель… (gemini-4-hydrogen думает до минуты)</p>`);
        const r = await api("api/llm/test", { method: "POST", body: { provider, model: f.model.value.trim(), key: Number(f.key.value) } });
        setHTML(out, html`<div class="svc-row"><span class="chip ${r.ok ? "chip-acc" : "chip-coral"}">${r.ok ? "отвечает" : "не отвечает"}</span>
          ${r.text ? html`<span>«${r.text}»</span>` : ""}${attemptsHTML(r.attempts)}</div>`);
        loadKeys();
      } else {
        const r = await api(`api/llm/models?provider=${encodeURIComponent(provider)}`);
        setHTML(f.querySelector("#ai-models"), html`${r.models.map((m) => html`<option value="${m}">`)}`);
        setHTML(out, html`<div class="svc-row"><span>Моделей у провайдера: <b>${r.models.length}</b> — они в подсказках поля «Модель».</span>
          <span class="muted">Проверенные: ${r.recommended.join(", ") || "—"}</span></div>`);
      }
    } catch (e) {
      setHTML(out, html``);
      toast(e.message, "error");
    } finally {
      busy(a, false);
    }
  });
}

export { icon };

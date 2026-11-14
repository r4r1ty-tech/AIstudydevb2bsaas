// «ИИ для тестов»: свой провайдер, модель и ключ. Общий для «Ещё» (каждый
// себе) и «Служебного» (админ видит ещё и выбор всех).
import { api } from "./api.js";
import { html, setHTML } from "./dom.js";
import { icon } from "./icons.js";
import { busy } from "./ui.js";
import { toast } from "./toast.js";

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
    </form></div>`;
}

// bindAI вешает обработчики на root; get/set — текущее состояние, rerender — перерисовать.
export function bindAI(root, get, set, rerender) {
  root.addEventListener("change", (ev) => {
    const f = ev.target.form;
    if (!f || f.id !== "ai-form" || ev.target.name !== "provider") return;
    const { models, keys } = optionsFor(get(), ev.target.value);
    f.model.value = "";
    f.model.placeholder = models[0] || "по умолчанию у провайдера";
    setHTML(f.querySelector("#ai-models"), html`${models.map((m) => html`<option value="${m}">`)}`);
    setHTML(f.key, html`<option value="0" selected>Любой (по очереди)</option>${keys.map((k) => html`<option value="${k.n}">${keyLabel(k)}</option>`)}`);
  });

  root.addEventListener("submit", async (ev) => {
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

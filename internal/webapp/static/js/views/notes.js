// «Конспекты»: записанные пары по предметам, статус и ссылки в GitHub.
import { api } from "../api.js";
import { html, safeHref, setHTML } from "../dom.js";
import { icon } from "../icons.js";
import { dateShort, plural } from "../format.js";
import { app, empty, errorBlock, head, kindLabel, skeleton } from "../ui.js";

const STATUS = {
  ready: { chip: "chip-acc", ico: "check", label: "Готов" },
  building: { chip: "chip-amber", ico: "clock", label: "Собирается", text: "Расшифровка и конспект в работе." },
  recording: { chip: "chip-coral", dot: true, label: "Пишется", text: "Пара идёт, звук записывается." },
  empty: { chip: "chip-ghost", ico: "x", label: "Запись пустая", text: "На записи нет звука — конспект собрать не из чего." },
  error: { chip: "chip-coral", ico: "warn", label: "Ошибка", text: "Конспект не собрался. Бот повторит попытку сам." },
};

function note(n) {
  const s = STATUS[n.status] || STATUS.building;
  const pdfHref = safeHref(n.pdf_url);
  const folderHref = safeHref(n.folder_url);
  return html`<div class="note">
    <div class="note-top">
      <p class="note-name">${kindLabel(n.kind, n.type)} ${n.number} <span class="mono">· ${dateShort(n.date)}</span></p>
      <span class="chip ${s.chip}">${s.dot ? html`<span class="rec-dot" aria-hidden="true"></span>` : icon(s.ico)}${s.label}</span>
    </div>
    ${n.topic ? html`<p class="note-topic">${n.topic}</p>` : ""}
    ${s.text ? html`<p class="${n.status === "error" ? "note-err" : "field-hint"}">${s.text}</p>` : ""}
    ${n.status === "ready" && !n.published ? html`<p class="field-hint">Конспект собран, в GitHub ещё не выгружен.</p>` : ""}
    ${pdfHref || folderHref
      ? html`<div class="note-actions">
          ${pdfHref ? html`<a class="btn btn-sm" href="${pdfHref}" target="_blank" rel="noopener noreferrer">${icon("file")}PDF</a>` : ""}
          ${folderHref ? html`<a class="btn btn-sm btn-ghost" href="${folderHref}" target="_blank" rel="noopener noreferrer">${icon("folder")}Папка в GitHub</a>` : ""}
        </div>`
      : ""}
  </div>`;
}

export function mountNotes(root) {
  let alive = true;
  setHTML(root, skeleton());

  async function load() {
    let d;
    try {
      d = await api("api/notes");
    } catch (e) {
      if (e.status === 401 || !alive) return;
      setHTML(root, html`${head({ title: "Конспекты" })}${errorBlock(e)}`);
      return;
    }
    if (!alive) return;
    const ns = d.subjects.length;
    const eyebrow = ns ? `${ns} ${plural(ns, "предмет", "предмета", "предметов")} · ${d.total} ${plural(d.total, "запись", "записи", "записей")}` : "";
    const repoHref = safeHref(d.repo_url);
    setHTML(root, html`${head({
      eyebrow,
      title: "Конспекты",
      side: repoHref ? html`<a class="btn btn-sm btn-ghost" href="${repoHref}" target="_blank" rel="noopener noreferrer">${icon("folder")}Все в GitHub</a>` : "",
    })}
      ${ns ? "" : empty({ ico: "notes", img: app.illus.empty, title: "Конспектов пока нет", text: "Когда бот запишет лекцию, после пары здесь появится конспект в PDF." })}
      ${d.subjects.map((s) => html`<section class="card subject" aria-label="${s.discipline}">
        <header class="subject-head"><h2 class="card-title">${s.discipline}</h2><span class="subject-count">${s.notes.length}</span></header>
        ${s.notes.map(note)}
      </section>`)}`);
  }

  root.addEventListener("click", (ev) => {
    if (ev.target.closest('[data-action="retry"]')) {
      setHTML(root, skeleton());
      load();
    }
  });
  load();
  return () => { alive = false; };
}

// Подтверждение действия на нативном <dialog>: фокус, Esc, «назад» и
// блокировку страницы даёт браузер. Возвращает Promise<boolean>.
import { html, str } from "./dom.js";

let seq = 0;

export function confirmDialog({ title, text = "", ok = "Да", cancel = "Отмена", tone = "primary" }) {
  if (typeof HTMLDialogElement !== "function" || !HTMLDialogElement.prototype.showModal) {
    return Promise.resolve(window.confirm(text ? `${title}\n\n${text}` : title));
  }
  const id = `dlg-${++seq}`;
  const t = document.createElement("template");
  t.innerHTML = str(html`<dialog class="dialog" closedby="any" aria-labelledby="${id}-t" ${text ? html`aria-describedby="${id}-d"` : ""}>
    <form method="dialog" class="dialog-body">
      <h2 class="dialog-title" id="${id}-t">${title}</h2>
      ${text ? html`<p class="dialog-text" id="${id}-d">${text}</p>` : ""}
      <div class="btn-row dialog-actions">
        <button class="btn" value="cancel" autofocus>${cancel}</button>
        <button class="btn btn-${tone}" value="ok">${ok}</button>
      </div>
    </form>
  </dialog>`);
  const dlg = t.content.firstElementChild;
  document.body.append(dlg);
  return new Promise((resolve) => {
    dlg.addEventListener("close", () => {
      resolve(dlg.returnValue === "ok");
      // Даём доиграть анимации закрытия, потом убираем узел.
      setTimeout(() => dlg.remove(), 300);
    }, { once: true });
    // Тап по затемнению закрывает окно сам через closedby="any"; где атрибут
    // не поддержан, ловим клик по самому <dialog> (вокруг формы padding нет).
    if (!("closedBy" in HTMLDialogElement.prototype)) {
      dlg.addEventListener("click", (ev) => {
        if (ev.target === dlg) dlg.close("cancel");
      });
    }
    dlg.showModal();
  });
}

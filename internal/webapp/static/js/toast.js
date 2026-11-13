import { html, str } from "./dom.js";
import { icon } from "./icons.js";

const box = () => document.getElementById("toasts");

export function toast(message, kind = "ok") {
  const root = box();
  if (!root) return;
  const el = document.createElement("div");
  el.className = `toast toast-${kind}`;
  el.innerHTML = str(html`${icon(kind === "error" ? "warn" : "check")}<span>${message}</span>`);
  root.append(el);
  while (root.children.length > 3) root.firstElementChild.remove();
  setTimeout(() => el.remove(), kind === "error" ? 5200 : 2600);
}

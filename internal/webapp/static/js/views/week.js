// «Неделя»: лента дней, выбранный день — те же карточки компактнее,
// переключатель на каждой паре и на весь день.
import { api } from "../api.js";
import { html, patch, setHTML } from "../dom.js";
import { addDays, dateLong, dateShort, dayParts, mondayOf, plural, syncClock, weekdayShort } from "../format.js";
import { empty, errorBlock, head, skeleton } from "../ui.js";
import { bindLessonActions, dayMode, lessonCard, willJoin } from "../lesson.js";
import { modeHint, modeSwitch } from "./today.js";
import { toast } from "../toast.js";

const MONTHS_GEN = ["января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"];

function rangeLabel(a, b) {
  const pa = dayParts(a);
  const pb = dayParts(b);
  if (pa.month === pb.month) return `${pa.day} – ${pb.day} ${MONTHS_GEN[pb.month]}`;
  return `${dateShort(a)} – ${dateShort(b)}`;
}

export function mountWeek(root, ctx) {
  const st = { data: null, byDate: new Map(), selected: ctx.sub || "", busy: 0, alive: true };
  setHTML(root, skeleton());

  const find = (id) => {
    for (const d of st.data.days) {
      const l = d.lessons.find((x) => x.id === id);
      if (l) return l;
    }
    return null;
  };
  const dayOf = (id) => st.data.days.find((d) => d.lessons.some((x) => x.id === id));

  const ctrl = bindLessonActions(root, {
    find,
    me: () => st.data.me,
    base: (id) => (dayOf(id) && dayOf(id).mode === "all" ? "yes" : "auto"),
    render,
    merge(res) {
      const d = dayOf(res.lesson.id);
      if (d) {
        const i = d.lessons.findIndex((x) => x.id === res.lesson.id);
        d.lessons[i] = res.lesson;
        d.mode = res.mode;
      }
      render();
    },
    busy(on) { st.busy += on ? 1 : -1; },
    reload: load,
  });

  async function load() {
    try {
      const d = await api("api/week");
      syncClock(d.now);
      if (!st.alive) return;
      st.data = d;
      st.byDate = new Map(d.days.map((x) => [x.date, x]));
      if (!st.selected || !validDay(st.selected)) {
        st.selected = st.byDate.has(d.today) ? d.today : (d.days.find((x) => x.date >= d.today) || d.days[0] || { date: d.today }).date;
      }
      st.error = null;
    } catch (e) {
      if (e.status === 401) return;
      st.error = e;
    }
    render(true);
  }

  function validDay(date) {
    return /^\d{4}-\d{2}-\d{2}$/.test(date);
  }

  function dots(day) {
    if (!day) return "";
    const mine = day.lessons.filter((l) => l.mine).slice(0, 6);
    return html`${mine.map((l) => html`<i class="${!l.online ? "person" : willJoin(l, st.data.me) ? "" : "off"}"></i>`)}`;
  }

  function render(full = false) {
    if (!st.alive) return;
    if (!st.data) {
      setHTML(root, html`${head({ title: "Неделя" })}${st.error ? errorBlock(st.error) : ""}`);
      return;
    }
    const d = st.data;
    const first = d.days.length ? d.days[0].date : d.today;
    const last = d.days.length ? d.days[d.days.length - 1].date : d.today;
    const start = mondayOf(first < d.today ? first : mondayOf(d.today));
    const endMonday = mondayOf(last > d.today ? last : d.today);
    const weeks = [];
    for (let m = start; m <= endMonday; m = addDays(m, 7)) weeks.push(m);
    if (full || !root.querySelector("#w-days")) {
      setHTML(root, html`<div id="w-head"></div><div class="week-grid" id="w-days"></div><section class="stack-lg" id="w-day" aria-live="polite"></section>`);
    }
    patch(root.querySelector("#w-head"), [{ key: "h", html: head({ eyebrow: rangeLabel(weeks[0], addDays(weeks[weeks.length - 1], 6)), title: "Неделя" }) }]);

    const thisMonday = mondayOf(d.today);
    patch(root.querySelector("#w-days"), weeks.map((m) => ({
      key: m,
      html: html`<div class="stack">
        <p class="week-row-label">${m === thisMonday ? "Эта неделя" : m === addDays(thisMonday, 7) ? "Следующая неделя" : m < thisMonday ? "Прошлая неделя" : rangeLabel(m, addDays(m, 6))}</p>
        <div class="week-days">${[0, 1, 2, 3, 4, 5, 6].map((i) => {
          const date = addDays(m, i);
          const day = st.byDate.get(date);
          const n = day ? day.lessons.filter((l) => l.mine).length : 0;
          const cls = ["wday"];
          if (!n) cls.push("is-empty");
          if (date < d.today) cls.push("is-past");
          if (date === d.today) cls.push("is-today");
          const label = `${dateLong(date)}${date === d.today ? ", сегодня" : ""}: ${n ? `${n} ${plural(n, "пара", "пары", "пар")}` : "пар нет"}`;
          return html`<button type="button" class="${cls.join(" ")}" data-day="${date}" aria-pressed="${date === st.selected ? "true" : "false"}" aria-label="${label}">
            <span class="wd">${weekdayShort(date)}</span><span class="dn">${dayParts(date).day}</span><span class="dots" aria-hidden="true">${dots(day)}</span>
          </button>`;
        })}</div>
      </div>`,
    })));

    const day = st.byDate.get(st.selected);
    const items = [];
    const mode = day ? dayMode(day.lessons) : "empty";
    items.push({
      key: `dh-${st.selected}`,
      html: html`<div class="day-head"><h2>${dateLong(st.selected)}${st.selected === d.today ? " · сегодня" : ""}</h2>
        ${mode !== "empty" && st.selected >= d.today ? html`<div class="daymode" data-date="${st.selected}">${modeSwitch(mode, { compact: true, date: st.selected, today: st.selected === d.today, label: `Режим дня: ${dateLong(st.selected)}` })}${modeHint(mode, st.selected === d.today)}</div>` : ""}
      </div>`,
    });
    if (!day || !day.lessons.length) {
      items.push({ key: "empty", html: empty({ ico: "calendar", title: "Пар нет", text: "В этот день расписание пустое." }) });
    } else {
      items.push({ key: "legend", html: html`<div class="legend" aria-hidden="true"><span><i></i>бот зайдёт</span><span><i class="off"></i>бот не пойдёт</span><span><i class="person"></i>очная пара</span></div>` });
      for (const l of day.lessons) {
        items.push({ key: `l-${l.id}`, html: lessonCard(l, d.me, { compact: true, pending: ctrl.pending.has(l.id) }) });
      }
    }
    patch(root.querySelector("#w-day"), items);
  }

  root.addEventListener("click", async (ev) => {
    const dayBtn = ev.target.closest("[data-day]");
    if (dayBtn) {
      st.selected = dayBtn.dataset.day;
      history.replaceState(null, "", `#week/${st.selected}`);
      render();
      return;
    }
    const b = ev.target.closest("[data-mode]");
    if (b && st.data) {
      if (st.modeBusy || b.getAttribute("aria-checked") === "true") return;
      const date = b.closest("[data-date]").dataset.date;
      const day = st.byDate.get(date);
      if (!day) return;
      const mode = b.dataset.mode;
      const snapshot = day.lessons.map((l) => ({ ...l }));
      const dec = mode === "all" ? "yes" : mode === "none" ? "no" : "auto";
      for (const l of day.lessons) {
        if (l.online && l.mine && l.phase !== "past") l.decision = dec;
      }
      st.busy++;
      st.modeBusy = true;
      render();
      try {
        const res = await api("api/day", { method: "POST", body: { date, mode } });
        day.lessons = res.lessons;
        day.mode = res.mode;
        toast(mode === "none" ? "В этот день бот не заходит" : mode === "all" ? "Бот зайдёт на все онлайн-пары дня" : "Режим дня: как обычно");
      } catch (e) {
        day.lessons = snapshot;
        toast(`Не сохранилось: ${e.message}`, "error");
      } finally {
        st.busy--;
        st.modeBusy = false;
        render();
      }
      return;
    }
    if (ev.target.closest('[data-action="retry"]')) {
      setHTML(root, skeleton());
      load();
    }
  });

  load();
  return () => { st.alive = false; };
}


// Даты, длительности и склонения. Время показываем в поясе расписания.

let tz = "Europe/Samara";
let skew = 0; // серверное время минус время браузера, мс

export function setTimezone(name) {
  if (name) tz = name;
}

export function syncClock(serverISO) {
  const t = Date.parse(serverISO);
  if (!Number.isNaN(t)) skew = t - Date.now();
}

export function now() {
  return Date.now() + skew;
}

const MONTHS = ["января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"];
const WD_LONG = ["воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"];
const WD_SHORT = ["вс", "пн", "вт", "ср", "чт", "пт", "сб"];

function parseDay(d) {
  const [y, m, dd] = d.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, dd, 12));
}

export function dayParts(d) {
  const t = parseDay(d);
  return { wd: t.getUTCDay(), day: t.getUTCDate(), month: t.getUTCMonth(), year: t.getUTCFullYear() };
}

export function dateLong(d) {
  const p = dayParts(d);
  return `${WD_LONG[p.wd]}, ${p.day} ${MONTHS[p.month]}`;
}

export function dateShort(d) {
  const p = dayParts(d);
  return `${p.day} ${MONTHS[p.month]}`;
}

export function weekdayShort(d) {
  return WD_SHORT[dayParts(d).wd];
}

export function addDays(d, n) {
  const t = parseDay(d);
  t.setUTCDate(t.getUTCDate() + n);
  return t.toISOString().slice(0, 10);
}

export function mondayOf(d) {
  const wd = (dayParts(d).wd + 6) % 7;
  return addDays(d, -wd);
}

export function plural(n, one, few, many) {
  const a = Math.abs(n) % 100;
  const b = a % 10;
  if (a > 10 && a < 20) return many;
  if (b > 1 && b < 5) return few;
  if (b === 1) return one;
  return many;
}

// duration: «45 мин», «1 ч 20 мин», «2 дн 3 ч».
export function duration(ms) {
  const min = Math.max(0, Math.round(ms / 60000));
  if (min < 60) return `${min} мин`;
  const h = Math.floor(min / 60);
  const m = min % 60;
  if (h < 24) return m ? `${h} ч ${m} мин` : `${h} ч`;
  const d = Math.floor(h / 24);
  const hh = h % 24;
  return hh ? `${d} дн ${hh} ч` : `${d} ${plural(d, "день", "дня", "дней")}`;
}

const timeFmt = () => new Intl.DateTimeFormat("ru-RU", { timeZone: tz, hour: "2-digit", minute: "2-digit" });
const dayKeyFmt = () => new Intl.DateTimeFormat("en-CA", { timeZone: tz, year: "numeric", month: "2-digit", day: "2-digit" });

export function timeOf(iso) {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? "—" : timeFmt().format(t);
}

export function dayKey(ms) {
  return dayKeyFmt().format(ms);
}

// whenLabel: «сегодня в 23:59», «завтра в 18:00», «пт, 14 ноября в 12:00».
export function whenLabel(iso) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const today = dayKey(now());
  const k = dayKey(t);
  const at = timeFmt().format(t);
  if (k === today) return `сегодня в ${at}`;
  if (k === addDays(today, 1)) return `завтра в ${at}`;
  if (k === addDays(today, -1)) return `вчера в ${at}`;
  return `${weekdayShort(k)}, ${dateShort(k)} в ${at}`;
}

export function ago(iso) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const ms = now() - t;
  if (ms < 60000) return "только что";
  return `${duration(ms)} назад`;
}

export function stamp(iso) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const k = dayKey(t);
  return `${dateShort(k)}, ${timeFmt().format(t)}`;
}

export function bytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return `${n} Б`;
  if (n < 1048576) return `${(n / 1024).toFixed(0)} КБ`;
  return `${(n / 1048576).toFixed(1).replace(".", ",")} МБ`;
}

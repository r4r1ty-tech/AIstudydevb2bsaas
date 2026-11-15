// Свои inline-иконки: сетка 24, линия 1.8, скруглённые концы.
import { raw } from "./dom.js";

const P = {
  today: '<circle cx="12" cy="12" r="3.2"/><path d="M12 3v2.2M12 18.8V21M3 12h2.2M18.8 12H21M5.6 5.6l1.6 1.6M16.8 16.8l1.6 1.6M5.6 18.4l1.6-1.6M16.8 7.2l1.6-1.6"/>',
  week: '<rect x="3.5" y="5" width="17" height="15" rx="3"/><path d="M3.5 9.5h17M8 3v4M16 3v4"/><path d="M7.5 13.5h2M11 13.5h2M14.5 13.5h2M7.5 16.5h2M11 16.5h2"/>',
  due: '<path d="M6 3.5h12M6 20.5h12"/><path d="M7.5 3.5c0 4.2 4.5 5.3 4.5 8.5s-4.5 4.3-4.5 8.5M16.5 3.5c0 4.2-4.5 5.3-4.5 8.5s4.5 4.3 4.5 8.5"/><path d="M9.5 18h5"/>',
  notes: '<path d="M14 3.5H7.5a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2V8z"/><path d="M14 3.5V8h4.5M9 12.5h6M9 16h4"/>',
  more: '<circle cx="5.5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="18.5" cy="12" r="1.6"/>',
  lecture: '<rect x="3.5" y="4" width="17" height="11" rx="2"/><path d="M12 15v5M8.5 20h7M7.5 8.5h5M7.5 11.5h8"/>',
  practice: '<path d="M4.5 19.5l1-4L15.8 5.2a2 2 0 0 1 2.9 0l.1.1a2 2 0 0 1 0 2.9L8.5 18.5z"/><path d="M13.8 7.2l3 3"/>',
  lab: '<path d="M9.5 3.5h5M10.5 3.5v5.2L5 18a1.8 1.8 0 0 0 1.6 2.5h10.8A1.8 1.8 0 0 0 19 18l-5.5-9.3V3.5"/><path d="M7.4 14.5h9.2"/>',
  other: '<circle cx="12" cy="12" r="8.5"/><path d="M12 8v4.5l3 1.8"/>',
  online: '<path d="M3.5 9.2a12.5 12.5 0 0 1 17 0M6.5 12.5a8 8 0 0 1 11 0M9.5 15.7a3.6 3.6 0 0 1 5 0"/><circle cx="12" cy="19" r="1.1"/>',
  place: '<path d="M12 21s-6.5-6-6.5-11a6.5 6.5 0 0 1 13 0c0 5-6.5 11-6.5 11z"/><circle cx="12" cy="10" r="2.3"/>',
  bot: '<path d="M4.5 14v-1.5a7.5 7.5 0 0 1 15 0V14"/><rect x="3.5" y="13.5" width="4" height="6.5" rx="1.6"/><rect x="16.5" y="13.5" width="4" height="6.5" rx="1.6"/>',
  rec: '<circle cx="12" cy="12" r="8.5"/><circle cx="12" cy="12" r="3.5" fill="currentColor" stroke="none"/>',
  link: '<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>',
  nolink: '<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/><path d="M4 4l16 16"/>',
  warn: '<path d="M12 4.2L21 19.5H3z"/><path d="M12 10v4.2M12 16.9v.1"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  x: '<path d="M6.5 6.5l11 11M17.5 6.5l-11 11"/>',
  clock: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/>',
  ext: '<path d="M13.5 4.5h6v6M19.5 4.5L11 13"/><path d="M17.5 13.5v4a2 2 0 0 1-2 2h-9a2 2 0 0 1-2-2v-9a2 2 0 0 1 2-2h4"/>',
  folder: '<path d="M3.5 7.5a2 2 0 0 1 2-2h4l2 2.5h7a2 2 0 0 1 2 2v7.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"/>',
  file: '<path d="M14 3.5H7.5a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2V8z"/><path d="M14 3.5V8h4.5"/><path d="M9 15.5h6"/>',
  quiz: '<circle cx="12" cy="12" r="8.5"/><path d="M9.6 9.5a2.5 2.5 0 0 1 4.8.9c0 1.7-2.4 2-2.4 3.6M12 16.9v.1"/>',
  assign: '<path d="M8 4.5h8M9 3.5h6v2.5H9z"/><rect x="5" y="4.5" width="14" height="16" rx="2"/><path d="M8.5 12.5l2.3 2.3 4.7-4.7"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="M11 12l8-8M16 7l2.5 2.5M14 9l1.8 1.8"/>',
  user: '<circle cx="12" cy="8.5" r="4"/><path d="M4.5 20.5a7.5 7.5 0 0 1 15 0"/>',
  door: '<rect x="5" y="3.5" width="12" height="17" rx="1.5"/><path d="M17 20.5h2.5M14 12v.1"/>',
  tools: '<path d="M14.5 6.5a4 4 0 0 0-5.3 5.3L4 17l3 3 5.2-5.2a4 4 0 0 0 5.3-5.3l-2.4 2.4-2.6-.4-.4-2.6z"/>',
  chev: '<path d="M6.5 9.5l5.5 5.5 5.5-5.5"/>',
  refresh: '<path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3M19.5 4.5v4h-4"/>',
  wifioff: '<path d="M3.5 9.2a12.5 12.5 0 0 1 4.3-2.6M12 6.5a12.5 12.5 0 0 1 8.5 2.7M6.5 12.5a8 8 0 0 1 3-1.8M9.5 15.7a3.6 3.6 0 0 1 5 0"/><circle cx="12" cy="19" r="1.1"/><path d="M4 4l16 16"/>',
  hourglass: '<path d="M6 3.5h12M6 20.5h12M7.5 3.5c0 4.2 4.5 5.3 4.5 8.5s-4.5 4.3-4.5 8.5M16.5 3.5c0 4.2-4.5 5.3-4.5 8.5s4.5 4.3 4.5 8.5"/>',
  moon: '<path d="M19.5 14.5A8 8 0 0 1 9.5 4.5a8 8 0 1 0 10 10z"/>',
  calendar: '<rect x="3.5" y="5" width="17" height="15" rx="3"/><path d="M3.5 9.5h17M8 3v4M16 3v4"/>',
  power: '<path d="M12 3.5v8"/><path d="M7 6.3a7.5 7.5 0 1 0 10 0"/>',
  logout: '<path d="M14 4.5h3.5a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H14M10 16l4-4-4-4M14 12H4.5"/>',
};

export function icon(name, cls = "") {
  const d = P[name] || P.other;
  return raw(`<svg class="icon ${cls}" viewBox="0 0 24 24" aria-hidden="true" focusable="false">${d}</svg>`);
}

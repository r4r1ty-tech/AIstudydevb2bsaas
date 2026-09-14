(function () {
  "use strict";

  var tg = window.Telegram && window.Telegram.WebApp;
  var gate = document.getElementById("gate");
  var app = document.getElementById("app");
  var toastEl = document.getElementById("toast");
  var currentTab = "test";
  var nowTimer = null;
  var toastT = null;
  var booted = false;
  var PW_KEY = "panel_pw";

  function password() {
    try { return sessionStorage.getItem(PW_KEY) || ""; } catch (e) { return ""; }
  }

  function setPassword(v) {
    try {
      if (v) sessionStorage.setItem(PW_KEY, v);
      else sessionStorage.removeItem(PW_KEY);
    } catch (e) {}
  }

  function showGate(msg) {
    app.hidden = true;
    gate.hidden = false;
    var p = document.getElementById("gate-msg");
    if (p) {
      p.hidden = !msg;
      p.textContent = msg || "";
    }
  }

  function boot() {
    if (booted) return;
    if (!password()) {
      showGate();
      return;
    }
    api("/api/settings").then(function (s) {
      if (booted) return;
      booted = true;
      tg = window.Telegram && window.Telegram.WebApp;
      if (tg) {
        try {
          tg.ready();
          tg.expand();
          tg.setHeaderColor("#0c0f14");
          tg.setBackgroundColor("#0c0f14");
        } catch (e) {}
      }
      gate.hidden = true;
      app.hidden = false;
      if (s) {
        var el = document.getElementById("hdr-meta");
        el.textContent = (s.group_code || "группа") + " · " + (s.group_id || "") + " · " + (s.timezone || "");
      }
      showTab("now");
    }).catch(function () {});
  }

  function toast(msg) {
    toastEl.hidden = false;
    toastEl.textContent = msg;
    clearTimeout(toastT);
    toastT = setTimeout(function () { toastEl.hidden = true; }, 2400);
  }

  function api(path, opts) {
    opts = opts || {};
    var headers = {
      "X-Panel-Password": password(),
      Accept: "application/json",
    };
    if (opts.headers) {
      Object.keys(opts.headers).forEach(function (k) { headers[k] = opts.headers[k]; });
    }
    var body = opts.body;
    if (body && typeof body === "object") {
      headers["Content-Type"] = "application/json; charset=utf-8";
      body = JSON.stringify(body);
    }
    return fetch(path, {
      method: opts.method || "GET",
      headers: headers,
      body: body,
    }).then(function (res) {
      return res.text().then(function (text) {
        var data = null;
        if (text) {
          try { data = JSON.parse(text); } catch (e) { data = { error: text }; }
        }
        if (res.status === 401) {
          booted = false;
          setPassword("");
          showGate("неверный пароль");
          throw new Error("unauthorized");
        }
        if (!res.ok) {
          var err = new Error((data && data.error) || ("HTTP " + res.status));
          err.status = res.status;
          err.body = data;
          throw err;
        }
        return data;
      });
    });
  }

  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function fmtTime(iso) {
    if (!iso) return "—";
    var d = new Date(iso);
    if (isNaN(d.getTime())) return esc(iso);
    function p(n) { return n < 10 ? "0" + n : "" + n; }
    return p(d.getDate()) + "." + p(d.getMonth() + 1) + " " + p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
  }

  function lessonBlock(l) {
    if (!l) return '<p class="empty">нет</p>';
    var place = l.online
      ? '<span class="badge acc">online</span>'
      : '<span class="badge">очно</span>';
    var sub = l.subgroup ? "подгр. " + l.subgroup : "вся группа";
    return (
      '<div class="row-item">' +
        place +
        '<span class="mono">' + esc(l.start) + "–" + esc(l.end) + "</span>" +
        "<strong>" + esc(l.discipline) + "</strong>" +
        '<span class="muted">' + esc(l.teacher) + "</span>" +
        '<span class="badge">' + esc(sub) + "</span>" +
        '<span class="muted">' + esc(l.place || "") + "</span>" +
        (l.bbb_url ? '<a href="' + esc(l.bbb_url) + '">BBB</a>' : "") +
      "</div>"
    );
  }

  function renderNow(data) {
    var box = document.getElementById("now-accounts");
    var acc = (data && data.accounts) || [];
    if (!acc.length) {
      box.innerHTML = '<p class="empty">пусто</p>';
    } else {
      box.innerHTML = acc.map(function (a) {
        var nick = a.username ? "@" + a.username : (a.first_name || "");
        var fio = a.fio || "нет ФИО";
        var st = a.in_bot
          ? '<span class="badge ok">в боте</span>'
          : '<span class="badge">не в боте</span>';
        var prof = a.has_profile ? "" : '<span class="badge warn">нет профиля</span>';
        var bbb = '<span class="badge">bbb: ' + esc(a.in_bbb || "none") + "</span>";
        return (
          '<div class="row-item">' +
            '<span class="mono">' + esc(a.telegram_id) + "</span>" +
            "<span>" + esc(nick) + "</span>" +
            '<span class="muted">' + esc(fio) + "</span>" +
            st + prof + bbb +
          "</div>"
        );
      }).join("");
    }
    document.getElementById("now-current").innerHTML = lessonBlock(data.current);
    document.getElementById("now-next").innerHTML = lessonBlock(data.next);
    document.getElementById("now-rec").innerHTML = data.recording
      ? '<span class="badge ok">пишется</span>'
      : '<span class="badge">нет</span>';
    document.getElementById("poll-st").textContent = "обновлено " + new Date().toLocaleTimeString("ru-RU");
  }

  function loadNow() {
    return api("/api/now").then(renderNow).catch(function (e) {
      document.getElementById("poll-st").textContent = "ошибка";
      if (e.message !== "unauthorized" && e.message !== "forbidden") toast(e.message || "ошибка /now");
    });
  }

  function untilLabel(u) {
    if (!u || !u.disabled_until) return "";
    var d = new Date(u.disabled_until);
    if (isNaN(d.getTime()) || d.getTime() < Date.now()) return "";
    return '<span class="badge warn">до ' + fmtTime(u.disabled_until) + "</span>";
  }

  function renderPeople(data) {
    var root = document.getElementById("people-list");
    var people = (data && data.people) || [];
    if (!people.length) {
      root.innerHTML = '<p class="empty">пусто</p>';
      return;
    }
    root.innerHTML = people.map(function (p) {
      var u = p.user || { telegram_id: p.telegram_id };
      var id = p.telegram_id || u.telegram_id;
      var nick = u.username ? "@" + u.username : [u.first_name, u.last_name].filter(Boolean).join(" ");
      var en = u.enabled ? "checked" : "";
      var wl = p.in_whitelist ? '<span class="badge acc">whitelist</span>' : '<span class="badge">не в списке</span>';
      var prof = p.has_profile ? "" : '<span class="badge warn">нет профиля</span>';
      return (
        '<article class="card" data-id="' + esc(id) + '">' +
          '<div class="head">' +
            '<span class="mono">' + esc(id) + "</span>" +
            "<span>" + esc(nick) + "</span>" +
            wl + prof + untilLabel(u) +
            '<label class="toggle"><input type="checkbox" class="en" ' + en + '> вкл</label>' +
          "</div>" +
          '<label class="field">ФИО <input class="fio" value="' + esc(u.fio || "") + '"></label>' +
          '<label class="field">Подгруппа <input class="sub" type="number" min="0" max="4" value="' + esc(u.subgroup || 0) + '"></label>' +
          '<label class="field">SOCKS5 <input class="socks" value="' + esc(u.socks5 || "") + '" placeholder="user:pass@host:port"></label>' +
          '<label class="field">Вейкворды <input class="words" value="' + esc((u.extra_words || []).join(", ")) + '" placeholder="лаба, зачёт"></label>' +
          '<div class="actions">' +
            '<button type="button" class="act save">Сохранить</button>' +
            '<button type="button" class="act off-today">Выключить на сегодня</button>' +
            '<button type="button" class="act clear-off">Снять отключение</button>' +
          "</div>" +
        "</article>"
      );
    }).join("");
  }

  function loadPeople() {
    return api("/api/people").then(renderPeople).catch(function (e) {
      toast(e.message || "ошибка людей");
    });
  }

  function patchPerson(id, body) {
    return api("/api/people/" + id, { method: "POST", body: body }).then(function () {
      return loadPeople();
    });
  }

  document.getElementById("people-list").addEventListener("click", function (ev) {
    var card = ev.target.closest(".card");
    if (!card) return;
    var id = card.getAttribute("data-id");
    if (ev.target.classList.contains("save")) {
      patchPerson(id, {
        fio: card.querySelector(".fio").value,
        subgroup: Number(card.querySelector(".sub").value || 0),
        socks5: card.querySelector(".socks").value,
        extra_words: card.querySelector(".words").value,
      }).then(function () { toast("сохранено"); }).catch(function (e) { toast(e.message); });
    } else if (ev.target.classList.contains("off-today")) {
      patchPerson(id, { disable_today: true }).then(function () { toast("выключен на сегодня"); }).catch(function (e) { toast(e.message); });
    } else if (ev.target.classList.contains("clear-off")) {
      patchPerson(id, { disable_today: false }).then(function () { toast("отключение снято"); }).catch(function (e) { toast(e.message); });
    }
  });

  document.getElementById("people-list").addEventListener("change", function (ev) {
    if (!ev.target.classList.contains("en")) return;
    var card = ev.target.closest(".card");
    if (!card) return;
    patchPerson(card.getAttribute("data-id"), { enabled: !!ev.target.checked })
      .then(function () { toast("статус обновлён"); })
      .catch(function (e) { toast(e.message); });
  });

  function bytes(n) {
    n = Number(n) || 0;
    if (n < 1024) return n + " B";
    if (n < 1048576) return (n / 1024).toFixed(1) + " KB";
    return (n / 1048576).toFixed(1) + " MB";
  }

  function renderLessons(data) {
    var lessons = (data && data.lessons) || [];
    var box = document.getElementById("lessons-list");
    if (!lessons.length) {
      box.innerHTML = '<p class="empty">пар нет</p>';
    } else {
      box.innerHTML = lessons.map(function (l) {
        return lessonBlock(l) +
          '<div class="muted" style="margin:-4px 0 8px 4px">' + esc(l.date || "") +
          (l.type ? " · " + esc(l.type) : "") + "</div>";
      }).join("");
    }
    var bbb = (data && data.bbb) || [];
    var bb = document.getElementById("bbb-list");
    if (!bbb.length) {
      bb.innerHTML = '<p class="empty">ссылок нет</p>';
    } else {
      bb.innerHTML = bbb.map(function (b) {
        var key = b.key || "";
        var parts = key.split("|");
        var label = key;
        if (parts.length >= 3) {
          label = parts[1] + (parts[2] ? " · " + parts[2] : "");
        }
        return (
          '<div class="row-item">' +
            '<span class="grow">' + esc(label) + "</span>" +
            '<a href="' + esc(b.url) + '">' + esc(b.url) + "</a>" +
          "</div>"
        );
      }).join("");
    }
    var recs = (data && data.recordings) || [];
    var rb = document.getElementById("rec-list");
    if (!recs.length) {
      rb.innerHTML = '<p class="empty">файлов нет</p>';
    } else {
      rb.innerHTML = recs.map(function (f) {
        return (
          '<div class="row-item">' +
            '<span class="mono grow">' + esc(f.name) + (f.status ? " · " + esc(f.status) : "") + "</span>" +
            "<span>" + bytes(f.size) + "</span>" +
            '<span class="muted">' + fmtTime(f.mod) + "</span>" +
          "</div>"
        );
      }).join("");
    }
  }

  function loadLessons() {
    return api("/api/lessons").then(renderLessons).catch(function (e) { toast(e.message); });
  }

  document.getElementById("bbb-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var fd = new FormData(ev.target);
    var body = {
      key: (fd.get("key") || "").trim(),
      discipline: (fd.get("discipline") || "").trim(),
      teacher: (fd.get("teacher") || "").trim(),
      url: (fd.get("url") || "").trim(),
    };
    api("/api/bbb", { method: "POST", body: body })
      .then(function () {
        toast("ссылка сохранена");
        ev.target.reset();
        return loadLessons();
      })
      .catch(function (e) { toast(e.message); });
  });

  function renderParser(run) {
    run = run || {};
    var ok = run.ok
      ? '<span class="badge ok">ok</span>'
      : '<span class="badge bad">fail</span>';
    var at = run.at && run.at !== "0001-01-01T00:00:00Z" ? fmtTime(run.at) : "ещё не было";
    document.getElementById("parser-box").innerHTML =
      '<div class="row-item">' + ok +
      '<span>время: ' + esc(at) + "</span>" +
      '<span class="badge">' + esc(run.status || "—") + "</span>" +
      "<span>пар: " + esc(run.lesson_count || 0) + "</span>" +
      "<span>online: " + esc(run.online_count || 0) + "</span></div>";
    document.getElementById("parser-diff").textContent = run.diff || "(нет диффа)";
  }

  function loadParser() {
    return api("/api/parser").then(renderParser).catch(function (e) { toast(e.message); });
  }

  document.getElementById("btn-refresh").addEventListener("click", function () {
    var btn = this;
    btn.disabled = true;
    api("/api/parser/refresh", { method: "POST" })
      .then(function (run) {
        renderParser(run);
        toast("расписание обновлено");
      })
      .catch(function (e) {
        if (e.body) renderParser(e.body);
        toast(e.message || "ошибка рефреша");
      })
      .then(function () { btn.disabled = false; });
  });

  function renderLogs(data) {
    var evs = (data && data.events) || [];
    var root = document.getElementById("logs-list");
    if (!evs.length) {
      root.innerHTML = '<p class="empty">событий нет</p>';
      return;
    }
    root.innerHTML = evs.map(function (e) {
      return (
        '<div class="ev">' +
          '<div><span class="when">' + fmtTime(e.at) + "</span> " +
          '<span class="typ">' + esc(e.type) + "</span>" +
          '<span class="mono">' + esc(e.telegram_id || "") + "</span></div>" +
          "<div>" + esc(e.message) + "</div>" +
        "</div>"
      );
    }).join("");
  }

  function loadLogs() {
    return api("/api/logs?limit=200").then(renderLogs).catch(function (e) { toast(e.message); });
  }

  function testBadge(j) {
    var st = (j && j.status) || "idle";
    if (st === "room") return '<span class="badge ok">в комнате</span>';
    if (st === "lobby") return '<span class="badge warn">лобби</span>';
    if (st === "joining") return '<span class="badge acc">захожу</span>';
    if (st === "error") return '<span class="badge bad">ошибка</span>';
    return '<span class="badge">не в комнате</span>';
  }

  function renderTest(j) {
    j = j || {};
    var urlEl = document.getElementById("test-url");
    if (urlEl && j.url && !urlEl.value) urlEl.value = j.url;
    var mode = "";
    if (j.want === "listen" || j.mode === "listen") mode = " · слушаю";
    else if (j.want === "dummy" || j.mode === "dummy") mode = " · болванчик";
    var line = (j.message || "") + mode;
    document.getElementById("test-status").innerHTML =
      '<div class="row-item">' + testBadge(j) +
      '<span class="grow">' + esc(line || "кинь ссылку и выбери заход") + "</span></div>" +
      (j.url ? '<div class="muted" style="margin-top:8px">' + esc(j.url) + "</div>" : "") +
      '<div class="muted" style="margin-top:6px">в списке: ' + esc(j.name || "тест") + "</div>";
    var inRoom = j.want && (j.status === "joining" || j.status === "lobby" || j.status === "room");
    var dummyBtn = document.getElementById("test-dummy");
    var listenBtn = document.getElementById("test-listen");
    var leaveBtn = document.getElementById("test-leave");
    if (dummyBtn) {
      dummyBtn.classList.toggle("on", inRoom && j.want === "dummy");
      dummyBtn.disabled = false;
    }
    if (listenBtn) {
      listenBtn.classList.toggle("on", inRoom && j.want === "listen");
      listenBtn.disabled = false;
    }
    if (leaveBtn) leaveBtn.disabled = !inRoom && j.status !== "error";
  }

  function loadTest() {
    return api("/api/test").then(renderTest).catch(function (e) { toast(e.message); });
  }

  var testWant = "dummy";
  document.getElementById("test-form").addEventListener("click", function (ev) {
    var b = ev.target.closest("button[data-want]");
    if (b) testWant = b.getAttribute("data-want");
  });
  document.getElementById("test-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var url = (document.getElementById("test-url").value || "").trim();
    api("/api/test", { method: "POST", body: { url: url, want: testWant } })
      .then(function (j) {
        renderTest(j);
        toast(testWant === "listen" ? "заход со звуком" : "болванчик");
      })
      .catch(function (e) { toast(e.message); });
  });
  document.getElementById("test-leave").addEventListener("click", function () {
    api("/api/test", { method: "POST", body: { want: "leave" } })
      .then(function (j) { renderTest(j); toast("выходим"); })
      .catch(function (e) { toast(e.message); });
  });

  function stopNowPoll() {
    if (nowTimer) {
      clearInterval(nowTimer);
      nowTimer = null;
    }
  }

  function startNowPoll() {
    stopNowPoll();
    if (currentTab === "now") loadNow();
    if (currentTab === "test") loadTest();
    nowTimer = setInterval(function () {
      if (document.visibilityState !== "visible") return;
      if (currentTab === "now") loadNow();
      if (currentTab === "test") loadTest();
    }, 4000);
  }

  function showTab(name) {
    currentTab = name;
    document.querySelectorAll(".tabs button").forEach(function (b) {
      b.classList.toggle("active", b.getAttribute("data-tab") === name);
    });
    document.querySelectorAll("section.tab").forEach(function (sec) {
      sec.hidden = sec.getAttribute("data-panel") !== name;
    });
    if (name === "now" || name === "test") startNowPoll();
    else {
      stopNowPoll();
      if (name === "people") loadPeople();
      if (name === "lessons") loadLessons();
      if (name === "parser") loadParser();
      if (name === "logs") loadLogs();
    }
    if (name === "test") loadTest();
  }

  document.querySelector(".tabs").addEventListener("click", function (ev) {
    var b = ev.target.closest("button[data-tab]");
    if (!b) return;
    showTab(b.getAttribute("data-tab"));
  });

  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "visible" && currentTab === "now") loadNow();
    if (document.visibilityState === "visible" && currentTab === "test") loadTest();
  });

  document.getElementById("login-form").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var inp = document.getElementById("pw");
    var v = (inp && inp.value) ? inp.value.trim() : "";
    if (!v) {
      showGate("введи пароль");
      return;
    }
    setPassword(v);
    boot();
  });

  (function kickoff() {
    tg = window.Telegram && window.Telegram.WebApp;
    if (tg) {
      try { tg.ready(); } catch (e) {}
    }
    if (password()) boot();
    else showGate();
  })();
})();

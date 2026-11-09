// The command palette.
//
// Ctrl+K (⌘K on a Mac), or / when nothing is being typed, opens it on any
// page; so does the key cap in the bottom-left corner. Type a few letters of
// an application, a page, a section of the settings or something to do, and
// Enter goes there. Tab on an application lists what can be done with it.
//
// What it matches against is asked of the server each time it opens
// (/palette), so an application's state is the one it has now, and a viewer is
// never offered what only an admin may do. What was chosen last is kept in
// this browser and offered first next time.
//
// An action is sent the way a button on its page would send it: as a form,
// through confirm.js when it deserves a question, and answered with a toast.
(function () {
  var dialog = document.getElementById("palette");
  if (!dialog || typeof dialog.showModal !== "function") return;
  var input = dialog.querySelector("input");
  var list = dialog.querySelector(".palette-list");
  var scopeTag = dialog.querySelector(".palette-scope");

  var RECENT_KEY = "quasar.palette.recent";
  var RECENT_MAX = 5;
  var SUGGESTED = ["page:/apps/new", "page:/notifications", "do:/system/backup", "page:/logs", "page:/settings"];

  var INDEX_KEY = "quasar.palette.index";
  var index = null; // the server's answer
  var fresh = false; // whether index is this opening's answer
  var pending = []; // Enter and Tab pressed before it came, each with the query then
  var items = []; // what is on screen, in order
  var sel = 0;
  var scope = null; // the application whose actions are listed

  if (/Mac|iPhone|iPad/.test(navigator.platform || "")) {
    document.querySelectorAll("[data-palette-mod]").forEach(function (k) { k.textContent = "⌘"; });
  }

  // --- Drawings ---------------------------------------------------------------

  var S = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">';
  var ICONS = {
    app: S + '<path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="m3 8 9 5 9-5M12 13v8"/></svg>',
    page: S + '<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/></svg>',
    section: S + '<path d="M4 6h16M4 12h10M4 18h7"/></svg>',
    run: S + '<path d="m13 2-9 12h8l-1 8 9-12h-8z"/></svg>',
    theme: S + '<circle cx="12" cy="12" r="9"/><path d="M12 3a9 9 0 0 0 0 18z" fill="currentColor"/></svg>',
    logs: S + '<path d="M4 6h16M4 12h16M4 18h10"/></svg>',
    open: S + '<path d="M7 17 17 7M8 7h9v9"/></svg>',
    terminal: S + '<path d="m4 17 6-5-6-5M12 19h8"/></svg>',
    start: S + '<polygon points="7 4 19 12 7 20 7 4"/></svg>',
    stop: S + '<rect x="6" y="6" width="12" height="12" rx="1"/></svg>',
    restart: S + '<path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/></svg>',
    deploy: S + '<path d="M12 19V5"/><path d="m5 12 7-7 7 7"/></svg>',
    redeploy: S + '<path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/></svg>',
    update: S + '<path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/></svg>',
    overview: S + '<rect x="3" y="3" width="7" height="9" rx="1"/><rect x="14" y="3" width="7" height="5" rx="1"/><rect x="14" y="12" width="7" height="9" rx="1"/><rect x="3" y="16" width="7" height="5" rx="1"/></svg>',
    history: S + '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/><path d="M12 7v5l3 2"/></svg>',
    env: S + '<path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5a2 2 0 0 0 2 2h1"/><path d="M16 21h1a2 2 0 0 0 2-2v-5a2 2 0 0 1 2-2 2 2 0 0 1-2-2V5a2 2 0 0 0-2-2h-1"/></svg>',
    settings: S + '<circle cx="12" cy="12" r="3"/><path d="M12 2v3M12 19v3M4.9 4.9l2.1 2.1M17 17l2.1 2.1M2 12h3M19 12h3M4.9 19.1 7 17M17 7l2.1-2.1"/></svg>',
    dashboard: S + '<path d="m3 11 9-8 9 8"/><path d="M5 9.5V20a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9.5"/></svg>',
    plus: S + '<path d="M12 5v14M5 12h14"/></svg>',
    bell: S + '<path d="M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 0 1-3.4 0"/></svg>',
    audit: S + '<path d="M12 3 4 6v6c0 5 3.5 8 8 9 4.5-1 8-4 8-9V6z"/><path d="m9 12 2 2 4-4"/></svg>',
    server: S + '<rect x="3" y="4" width="18" height="7" rx="1"/><rect x="3" y="13" width="18" height="7" rx="1"/><path d="M7 7.5h.01M7 16.5h.01"/></svg>',
    copy: S + '<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>',
    archive: S + '<rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8"/><path d="M10 12h4"/></svg>',
  };

  // The pages that have a drawing of their own; the rest are a page or, inside
  // one, a section.
  var PAGE_ICONS = {
    "/": "dashboard", "/apps/new": "plus", "/notifications": "bell", "/logs": "logs", "/audit": "audit",
    "/system": "server", "/system#backups": "archive", "/settings": "settings", "/stations": "app",
  };

  // --- What there is to find --------------------------------------------------

  function appItem(a) {
    return {
      key: "app:" + a.id, group: "Applications", icon: "app", title: a.name, within: a.host || "",
      href: "/apps/" + encodeURIComponent(a.id), app: a, state: a.state,
    };
  }

  function everything() {
    var out = [];
    index.apps.forEach(function (a) { out.push(appItem(a)); });
    index.pages.forEach(function (p) {
      out.push({ key: "page:" + p.href, group: "Pages", icon: PAGE_ICONS[p.href] || (p.within ? "section" : "page"), title: p.title, within: p.within || "", href: p.href, words: p.words || "" });
    });
    index.actions.forEach(function (d) {
      out.push({ key: "do:" + d.action, group: "Actions", icon: "run", title: d.title, within: d.within || "", action: d.action, confirm: d.confirm || "", verb: d.title, words: d.words || "" });
    });
    index.themes.forEach(function (t) {
      out.push({ key: "theme:" + t.id, group: "Themes", icon: "theme", title: "Theme: " + t.name, within: t.current ? "current" : "", action: "/theme", fields: { theme: t.id }, words: "appearance colours dark light" });
    });
    return out;
  }

  // What can be done with one application: going to its pages for anybody,
  // acting on it for an admin, in the order its own page offers them.
  function appActions(a) {
    var base = "/apps/" + encodeURIComponent(a.id);
    var out = [];
    // First for a game server or a database: there is no page to open, and the
    // address is what somebody came for.
    if (a.address) out.push({ icon: "copy", title: "Copy address", within: a.address + " · " + a.proto.toUpperCase(), copy: a.address });
    if (a.host) out.push({ key: "open:" + a.id, icon: "open", title: "Open " + a.host, within: "new tab", href: "https://" + a.host, newTab: true });
    out.push({ key: "app:" + a.id, icon: "overview", title: "Overview", href: base });
    out.push({ key: "app-logs:" + a.id, icon: "logs", title: "Logs", href: base + "#logs" });
    out.push({ key: "app-history:" + a.id, icon: "history", title: "History", within: "deployments, tasks", href: base + "#history" });
    if (index.admin) {
      out.push({ key: "app-env:" + a.id, icon: "env", title: "Environment", href: base + "#environment" });
      out.push({ key: "app-settings:" + a.id, icon: "settings", title: "Settings", within: "domains, health check, limits", href: base + "#settings" });
      out.push({ key: "app-terminal:" + a.id, icon: "terminal", title: "Terminal", href: base + "/terminal" });
      var up = a.state === "running" || a.state === "error";
      if (!up) out.push({ icon: "start", title: "Start", verb: "Start", action: base + "/start" });
      if (up) {
        out.push({ icon: "restart", title: "Restart", verb: "Restart", action: base + "/restart" });
        out.push({ icon: "stop", title: "Stop", verb: "Stop", danger: true, action: base + "/stop", confirm: "Stop " + a.name + "? It stays down until it is started again." });
      }
      var git = a.deployType === "git", local = a.deployType === "local";
      out.push({ icon: "redeploy", title: "Redeploy", verb: "Redeploy", action: base + "/redeploy",
        confirm: "Redeploy " + a.name + "? " + (git ? "The containers are replaced, running the same commit as now."
          : local ? "The containers are replaced, built again from the folder uploaded last."
          : "The container is replaced, running the same image as now.") });
      // A folder is only ever updated from the machine it is on, with qsr.
      if (!local) out.push({ icon: "update", title: "Update", within: git ? "pull and rebuild" : "pull the image again", verb: "Update", action: base + "/update",
        confirm: "Update " + a.name + "? " + (git ? "The repository is pulled and rebuilt from the branch head." : "Its images are pulled again.") + " Roll back from the deployment history if it goes wrong." });
    }
    out.forEach(function (it) { it.group = a.name; });
    return out;
  }

  // --- Matching ----------------------------------------------------------------

  // How well q matches text, and which letters did: the whole of it at the
  // start of a word beats it somewhere inside, which beats its letters in
  // order with gaps. Nothing is -1.
  function match(text, q) {
    var t = text.toLowerCase();
    var i = t.indexOf(q);
    if (i >= 0) {
      var at = [];
      for (var k = 0; k < q.length; k++) at.push(i + k);
      var wordStart = i === 0 || /[\s›\-_.\/]/.test(t[i - 1]);
      return { score: (wordStart ? 300 : 200) - i - t.length / 100, at: at };
    }
    var found = [];
    var from = 0;
    for (var j = 0; j < q.length; j++) {
      var c = q[j];
      if (c === " ") continue;
      var p = t.indexOf(c, from);
      if (p < 0) return null;
      found.push(p);
      from = p + 1;
    }
    var spread = found.length ? found[found.length - 1] - found[0] : 0;
    return { score: 100 - spread - t.length / 100, at: found };
  }

  function rank(pool, q) {
    var out = [];
    pool.forEach(function (it) {
      var m = match(it.title, q);
      if (!m) {
        var other = match((it.within || "") + " " + (it.words || ""), q);
        if (other && other.score >= 200) m = { score: other.score - 150, at: [] };
      }
      if (m) out.push({ it: it, score: m.score, at: m.at });
    });
    out.sort(function (a, b) { return b.score - a.score; });
    return out;
  }

  // --- Recents -------------------------------------------------------------------

  function recentKeys() {
    try { return JSON.parse(localStorage.getItem(RECENT_KEY)) || []; } catch (e) { return []; }
  }
  function remember(it) {
    if (!it.key) return;
    try {
      var keys = recentKeys().filter(function (k) { return k !== it.key; });
      keys.unshift(it.key);
      localStorage.setItem(RECENT_KEY, JSON.stringify(keys.slice(0, RECENT_MAX)));
    } catch (e) {}
  }

  // --- Drawing --------------------------------------------------------------------

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text) e.textContent = text;
    return e;
  }

  function titleWithMarks(title, at) {
    var span = el("span", "palette-title");
    var marks = {};
    (at || []).forEach(function (i) { marks[i] = true; });
    var run = "", on = false;
    function flush() {
      if (!run) return;
      span.appendChild(on ? el("mark", "", run) : document.createTextNode(run));
      run = "";
    }
    for (var i = 0; i < title.length; i++) {
      if (!!marks[i] !== on) { flush(); on = !!marks[i]; }
      run += title[i];
    }
    flush();
    return span;
  }

  var STATE_TONE = { running: "ok", error: "err", deploying: "warn", restarting: "warn" };

  function draw(rows) {
    items = [];
    list.textContent = "";
    var group = null;
    rows.forEach(function (r) {
      if (r.it.group !== group) {
        group = r.it.group;
        list.appendChild(el("p", "palette-group", group));
      }
      var it = r.it;
      var row = el("div", "palette-row" + (it.danger ? " is-danger" : ""));
      row.id = "palette-opt-" + items.length;
      row.setAttribute("role", "option");
      row.dataset.i = items.length;
      var ic = el("span", "palette-icon");
      ic.innerHTML = ICONS[it.icon] || ICONS.page;
      row.appendChild(ic);
      var text = el("span", "palette-text");
      text.appendChild(titleWithMarks(it.title, r.at));
      if (it.within) text.appendChild(el("span", "palette-within", it.within));
      row.appendChild(text);
      var right = el("span", "palette-right");
      if (it.state) {
        var dot = el("span", "palette-dot palette-dot-" + (STATE_TONE[it.state] || "off"));
        right.appendChild(dot);
        right.appendChild(document.createTextNode(it.state));
      }
      if (it.app) right.appendChild(el("kbd", "palette-tab", "Tab"));
      row.appendChild(right);
      list.appendChild(row);
      items.push(it);
    });
    if (!items.length) {
      list.appendChild(el("p", "palette-empty", index ? "Nothing matches." : "Loading…"));
    }
    select(0);
  }

  function select(i) {
    if (!items.length) { input.removeAttribute("aria-activedescendant"); return; }
    sel = (i + items.length) % items.length;
    list.querySelectorAll(".palette-row").forEach(function (r) {
      var on = Number(r.dataset.i) === sel;
      r.classList.toggle("is-on", on);
      r.setAttribute("aria-selected", on ? "true" : "false");
      if (on) {
        input.setAttribute("aria-activedescendant", r.id);
        r.scrollIntoView({ block: "nearest" });
      }
    });
  }

  function refresh() {
    var q = input.value.trim().toLowerCase();
    if (scope) {
      var pool = appActions(scope);
      draw(q ? rank(pool, q) : pool.map(function (it) { return { it: it }; }));
      return;
    }
    if (!index) { draw([]); return; }
    var all = everything();
    if (!q) {
      var byKey = {};
      all.forEach(function (it) { byKey[it.key] = it; });
      // An application's own pages, chosen from its actions, come back too.
      index.apps.forEach(function (a) {
        appActions(a).forEach(function (it) {
          if (it.key && !byKey[it.key]) byKey[it.key] = Object.assign({}, it, { title: a.name + " › " + it.title });
        });
      });
      var rows = [];
      recentKeys().forEach(function (k) {
        if (!byKey[k]) return;
        var it = Object.assign({}, byKey[k], { group: "Recent" });
        rows.push({ it: it });
      });
      var shown = {};
      rows.forEach(function (r) { shown[r.it.key] = true; });
      all.filter(function (it) { return it.group === "Applications" && !shown[it.key]; }).slice(0, 8)
        .forEach(function (it) { rows.push({ it: it }); });
      // A few places most visits are for, until there is a history to offer.
      SUGGESTED.forEach(function (k) {
        if (byKey[k] && !shown[k]) rows.push({ it: Object.assign({}, byKey[k], { group: "Suggested" }) });
      });
      draw(rows);
      return;
    }
    var ranked = rank(all, q);
    // Grouped, the groups in the order of their best match.
    var order = [];
    var groups = {};
    ranked.forEach(function (r) {
      if (!groups[r.it.group]) { groups[r.it.group] = []; order.push(r.it.group); }
      if (groups[r.it.group].length < 8) groups[r.it.group].push(r);
    });
    var rows2 = [];
    order.forEach(function (g) { rows2 = rows2.concat(groups[g]); });
    rows2.push({ it: { key: "", group: "Logs", icon: "logs", title: "Search the logs for “" + input.value.trim() + "”", href: "/logs?q=" + encodeURIComponent(input.value.trim()) } });
    draw(rows2);
  }

  // --- Doing it ---------------------------------------------------------------------

  function enterScope(a) {
    scope = a;
    scopeTag.textContent = a.name;
    scopeTag.hidden = false;
    input.value = "";
    input.placeholder = "What to do with " + a.name + "…";
    refresh();
  }
  function leaveScope() {
    scope = null;
    scopeTag.hidden = true;
    input.value = "";
    input.placeholder = "Search applications, pages, actions…";
    refresh();
  }

  function run(it) {
    if (!it) return;
    remember(it);
    close();
    if (it.copy) {
      if (window.quasarCopy) window.quasarCopy(it.copy);
      return;
    }
    if (it.href) {
      if (it.newTab) window.open(it.href, "_blank", "noopener");
      else location.href = it.href;
      return;
    }
    var form = document.createElement("form");
    form.method = "post";
    form.action = it.action;
    form.hidden = true;
    Object.keys(it.fields || {}).forEach(function (k) {
      var f = document.createElement("input");
      f.type = "hidden";
      f.name = k;
      f.value = it.fields[k];
      form.appendChild(f);
    });
    // Named as the action, so a question asked about it says what it does.
    var b = document.createElement("button");
    b.type = "submit";
    b.textContent = it.verb || it.title;
    form.appendChild(b);
    if (it.confirm) form.dataset.confirm = it.confirm;
    document.body.appendChild(form);
    form.requestSubmit(b);
  }

  // --- Opening and closing ----------------------------------------------------------

  function open() {
    if (dialog.open) return;
    // The last answer, drawn at once while this opening's is asked for: the
    // box is never an empty "Loading…" under someone already typing.
    if (!index) {
      try { index = JSON.parse(sessionStorage.getItem(INDEX_KEY)); } catch (e) { index = null; }
    }
    fresh = false;
    pending = [];
    scope = null;
    scopeTag.hidden = true;
    input.value = "";
    input.placeholder = "Search applications, pages, actions…";
    refresh();
    dialog.showModal();
    input.focus();
    fetch("/palette", { headers: { Accept: "application/json" }, credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (data) {
        if (!data) return;
        index = data;
        fresh = true;
        try { sessionStorage.setItem(INDEX_KEY, JSON.stringify(data)); } catch (e) {}
        if (!dialog.open) return;
        // Scoped to an application from the last answer: the same one in
        // this one, with its state as it is now.
        if (scope) {
          var same = data.apps.filter(function (a) { return a.id === scope.id; })[0];
          if (same) scope = same; else { leaveScope(); }
        }
        // The keys pressed meanwhile, played again in order, each on what
        // had been typed when it was pressed: Tab on "next", then "stop" and
        // Enter, is Stop on nextcloud. What a Tab opened starts empty, so
        // what is typed after it filters that.
        var keys = pending.concat({ k: null, q: input.value });
        pending = [];
        var before = "";
        for (var i = 0; i < keys.length; i++) {
          var q = keys[i].q;
          if (q.indexOf(before) === 0) q = q.slice(before.length);
          if (!scope) q = before + q;
          input.value = q;
          refresh();
          before = keys[i].q;
          if (!keys[i].k) break;
          key(keys[i].k);
          if (!dialog.open) break;
        }
      })
      .catch(function () {});
  }
  function close() {
    if (dialog.open) dialog.close();
  }

  input.addEventListener("input", refresh);
  // Enter and Tab act on the row chosen; with nothing to choose from yet, the
  // list still on its way, they wait for it.
  function key(k) {
    var it = items[sel];
    if (!fresh && (!it || pending.length)) { pending.push({ k: k, q: input.value }); return; }
    if (k === "Enter") run(it);
    else if (it && it.app) enterScope(it.app);
  }

  input.addEventListener("keydown", function (e) {
    if (e.key === "ArrowDown") { e.preventDefault(); select(sel + 1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); select(sel - 1); }
    else if (e.key === "Enter") { e.preventDefault(); key("Enter"); }
    else if (e.key === "Tab") {
      e.preventDefault();
      if (e.shiftKey) { if (scope) leaveScope(); return; }
      key("Tab");
    } else if (e.key === "Backspace" && scope && input.value === "") {
      e.preventDefault();
      leaveScope();
    }
  });
  // Escape steps out of an application's actions before it closes the box.
  dialog.addEventListener("cancel", function (e) {
    if (scope) { e.preventDefault(); leaveScope(); }
  });
  list.addEventListener("mousemove", function (e) {
    var row = e.target.closest(".palette-row");
    if (row && Number(row.dataset.i) !== sel) select(Number(row.dataset.i));
  });
  list.addEventListener("click", function (e) {
    var row = e.target.closest(".palette-row");
    if (row) run(items[Number(row.dataset.i)]);
  });
  // A click on the backdrop, outside the box, closes it.
  dialog.addEventListener("click", function (e) {
    if (e.target === dialog) close();
  });
  document.querySelectorAll("[data-palette-open]").forEach(function (b) {
    b.addEventListener("click", open);
  });

  function typing(t) {
    return t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName));
  }
  document.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") {
      // A terminal has a Ctrl+K of its own.
      if (document.activeElement && document.activeElement.closest(".xterm")) return;
      e.preventDefault();
      if (dialog.open) close();
      else open();
    } else if (e.key === "/" && !e.ctrlKey && !e.metaKey && !e.altKey && !typing(e.target) && !dialog.open
      && !document.querySelector("dialog[open]")) {
      e.preventDefault();
      open();
    }
  });
})();

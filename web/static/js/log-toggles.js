// The reader's choices about what programs paint in their logs — the colours
// of their text, the backgrounds behind it: drawn, or left off where they make
// a theme unreadable. Kept in the browser — a preference about reading, like
// the theme is — and applied to <html>, so every pane on every page follows
// the one switch.
//
// The attributes are set by a line in the layout's head before anything is
// drawn; this keeps every switch on the page in step with them, and stores a
// change.
(function () {
  // Each switch: the <html> data attribute it sets, and where it is stored.
  var toggles = {
    colors: { attr: "logColors", key: "quasar.logColors" },
    bg: { attr: "logBg", key: "quasar.logBackgrounds" },
  };
  var root = document.documentElement;

  function sync() {
    document.querySelectorAll("[data-log-toggle]").forEach(function (box) {
      var t = toggles[box.dataset.logToggle];
      if (t) box.checked = root.dataset[t.attr] !== "off";
    });
  }

  document.addEventListener("change", function (e) {
    if (!e.target.matches || !e.target.matches("[data-log-toggle]")) return;
    var t = toggles[e.target.dataset.logToggle];
    if (!t) return;
    if (e.target.checked) delete root.dataset[t.attr];
    else root.dataset[t.attr] = "off";
    try {
      if (e.target.checked) localStorage.removeItem(t.key);
      else localStorage.setItem(t.key, "off");
    } catch (err) {
      /* remembered for this page only, which is the right failure */
    }
    sync();
  });

  sync();
  // A pane or a result list swapped in by htmx brings its own switches.
  document.addEventListener("htmx:afterSettle", sync);
})();

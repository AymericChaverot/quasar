// The reader's choice about the backgrounds programs paint in their logs:
// drawn, or left off where they make a theme unreadable. Kept in the browser
// — a preference about reading, like the theme is — and applied to <html>,
// so every pane on every page follows the one switch.
//
// The attribute is set by a line in the layout's head before anything is
// drawn; this keeps every switch on the page in step with it, and stores a
// change.
(function () {
  var key = "quasar.logBackgrounds";
  var root = document.documentElement;

  function sync() {
    var on = root.dataset.logBg !== "off";
    document.querySelectorAll("[data-log-bg-toggle]").forEach(function (box) {
      box.checked = on;
    });
  }

  document.addEventListener("change", function (e) {
    if (!e.target.matches || !e.target.matches("[data-log-bg-toggle]")) return;
    if (e.target.checked) delete root.dataset.logBg;
    else root.dataset.logBg = "off";
    try {
      if (e.target.checked) localStorage.removeItem(key);
      else localStorage.setItem(key, "off");
    } catch (err) {
      /* remembered for this page only, which is the right failure */
    }
    sync();
  });

  sync();
  // A pane or a result list swapped in by htmx brings its own switch.
  document.addEventListener("htmx:afterSettle", sync);
})();

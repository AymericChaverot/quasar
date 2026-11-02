// The lines around a line on the Logs page.
//
// A line's Context button opens the row under it on what its application
// wrote just before and just after, fetched the first time and kept, so
// closing and opening it again does not ask twice. Delegated, since the rows
// arrive with each page of results.
(function () {
  if (!window.htmx) return;
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-log-context]");
    if (!btn) return;
    var row = btn.closest("tr").nextElementSibling;
    if (!row || !row.classList.contains("log-ctx-row")) return;
    var open = btn.getAttribute("aria-expanded") === "true";
    btn.setAttribute("aria-expanded", open ? "false" : "true");
    btn.classList.toggle("is-on", !open);
    if (open) {
      row.hidden = true;
      return;
    }
    var cell = row.firstElementChild;
    if (cell.childElementCount) {
      row.hidden = false;
      return;
    }
    // Shown once it has something in it, so the table does not open on an
    // empty strip that then grows.
    htmx.ajax("GET", btn.getAttribute("data-log-context"), { target: cell, swap: "innerHTML" }).then(function () {
      if (btn.getAttribute("aria-expanded") === "true") row.hidden = false;
    });
  });
})();

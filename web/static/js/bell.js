// The header's bell.
//
// Its panel is fetched each time it opens rather than kept in the page: what
// is in it is the history as it is now, and drawing it is what marks it read.
// It closes on a click anywhere else, on Escape, and when a link in it is
// followed.
(function () {
  var bell = document.querySelector("[data-bell]");
  if (!bell || !window.htmx) return;
  var button = bell.querySelector(".bell-button");
  var panel = bell.querySelector(".bell-panel");

  // Shown once it is drawn, at its full size: an empty box that then grows
  // would make the page under it jump.
  var opening = false;
  function open() {
    if (opening) return;
    opening = true;
    button.setAttribute("aria-expanded", "true");
    htmx.ajax("GET", "/partials/bell", { target: panel, swap: "innerHTML" }).then(function () {
      opening = false;
      if (button.getAttribute("aria-expanded") === "true") panel.hidden = false;
    }, function () {
      opening = false;
      button.setAttribute("aria-expanded", "false");
    });
  }

  function close(focus) {
    if (button.getAttribute("aria-expanded") !== "true") return;
    panel.hidden = true;
    button.setAttribute("aria-expanded", "false");
    if (focus) button.focus();
  }

  // The tabs: every list is in the panel already, one is shown.
  panel.addEventListener("click", function (e) {
    var tab = e.target.closest("[data-bell-tab]");
    if (!tab) return;
    panel.querySelectorAll("[data-bell-tab]").forEach(function (t) {
      var on = t === tab;
      t.classList.toggle("is-on", on);
      t.setAttribute("aria-selected", on ? "true" : "false");
      var list = document.getElementById(t.getAttribute("aria-controls"));
      if (list) list.hidden = !on;
    });
  });

  button.addEventListener("click", function () {
    if (button.getAttribute("aria-expanded") === "true") close(false);
    else open();
  });
  document.addEventListener("click", function (e) {
    if (!bell.contains(e.target)) close(false);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !panel.hidden) close(true);
  });
})();

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

  function open() {
    panel.hidden = false;
    panel.classList.add("is-loading");
    button.setAttribute("aria-expanded", "true");
    htmx.ajax("GET", "/partials/bell", { target: panel, swap: "innerHTML" }).then(function () {
      panel.classList.remove("is-loading");
    });
  }

  function close(focus) {
    if (panel.hidden) return;
    panel.hidden = true;
    button.setAttribute("aria-expanded", "false");
    if (focus) button.focus();
  }

  button.addEventListener("click", function () {
    if (panel.hidden) open();
    else close(false);
  });
  document.addEventListener("click", function (e) {
    if (!bell.contains(e.target)) close(false);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !panel.hidden) close(true);
  });
})();

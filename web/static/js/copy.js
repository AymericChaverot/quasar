// Buttons that copy a piece of text: an address to give to somebody.
//
// Delegated, so a button that arrives in a swapped partial works as one in the
// page did. The button turns green for a moment to say it worked, since the
// clipboard itself shows nothing.
(function () {
  function done(btn, ok) {
    btn.classList.toggle("is-done", ok);
    var label = btn.getAttribute("aria-label");
    btn.setAttribute("title", ok ? "Copied" : "Could not copy — select it instead");
    clearTimeout(btn._copyTimer);
    btn._copyTimer = setTimeout(function () {
      btn.classList.remove("is-done");
      btn.setAttribute("title", label);
    }, 1500);
  }

  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-copy]");
    if (!btn) return;
    e.preventDefault();
    var text = btn.getAttribute("data-copy");
    if (!navigator.clipboard) return done(btn, false);
    navigator.clipboard.writeText(text).then(function () { done(btn, true); }, function () { done(btn, false); });
  });
})();

// Copying a piece of text: an address to give to somebody.
//
// A button with data-copy copies its value and turns green for a moment, since
// the clipboard itself shows nothing. The listener is delegated, so a button
// that arrives in a swapped partial works as one in the page did.
//
// quasarCopy is the same for a script with no button to turn green — the
// palette — and says so with a toast instead.
(function () {
  function write(text) {
    if (!navigator.clipboard) return Promise.reject(new Error("no clipboard"));
    return navigator.clipboard.writeText(text);
  }

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
    write(btn.getAttribute("data-copy")).then(function () { done(btn, true); }, function () { done(btn, false); });
  });

  window.quasarCopy = function (text) {
    write(text).then(function () {
      var box = document.getElementById("toasts");
      var tpl = document.getElementById("toast-copied");
      if (!box || !tpl) return;
      var t = tpl.content.firstElementChild.cloneNode(true);
      t.querySelector(".toast-text").textContent = text;
      box.append(t);
    }, function () {
      // Nothing was copied, and nothing else on screen holds the address:
      // the prompt does, selected, for a copy by hand.
      window.prompt("Copy this address", text);
    });
  };
})();

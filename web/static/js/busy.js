// The button that sent a request is shown working, and cannot be pressed
// again, until the answer is back.
//
// A restart takes as long as the container takes to stop, and a search over
// every stored line takes as long as that does; in between the page looked
// exactly as it had before the click, so it was clicked again, and the action
// went twice. Done here once rather than as hx-indicator and hx-disabled-elt
// on every control: the button is the one that was pressed — itself, or the
// submit button of the form that sent — and polls, which nobody pressed, have
// none.
//
// The spinner takes the icon's place (progress.css), so a button that has no
// spinner of its own is given one beside its icon the first time it is used.
(function () {
  var held = new WeakMap();

  function buttonOf(e) {
    var elt = e.detail.elt;
    if (elt instanceof HTMLButtonElement) return elt;
    if (elt instanceof HTMLFormElement) {
      var ev = e.detail.requestConfig && e.detail.requestConfig.triggeringEvent;
      return (ev && ev.submitter) || elt.querySelector('button[type="submit"], button:not([type])');
    }
    return null;
  }

  document.addEventListener("htmx:beforeRequest", function (e) {
    if (e.defaultPrevented) return;
    var btn = buttonOf(e);
    if (!btn || btn.disabled) return;
    if (!btn.querySelector(".spinner")) {
      var spin = document.createElement("span");
      spin.className = "spinner";
      spin.setAttribute("aria-hidden", "true");
      var ico = btn.querySelector(".ico");
      if (ico) ico.after(spin);
      else btn.prepend(spin);
    }
    btn.classList.add("htmx-request");
    btn.disabled = true;
    btn.setAttribute("aria-busy", "true");
    held.set(e.detail.elt, btn);
  });

  document.addEventListener("htmx:afterRequest", function (e) {
    var btn = held.get(e.detail.elt);
    if (!btn) return;
    held.delete(e.detail.elt);
    btn.classList.remove("htmx-request");
    btn.disabled = false;
    btn.removeAttribute("aria-busy");
  });
})();

// Polls only run where somebody can see them.
//
// Every panel that refreshes itself also listens for `quasar:seen`, which is
// what marks it as polled here. Each of its requests is asked first whether
// anybody can see it, and dropped on a no: a browser tab in the background, or
// a tab of the application page that is not open, has nobody reading it — and
// was costing the server a container listing, a stats read and a database
// query every few seconds all the same.
//
// (Not as an htmx trigger filter, `every 5s [seen(this)]`: htmx evaluates
// those with Function(), which the page's Content-Security-Policy forbids.)
//
// The other half is catching up. A panel that comes back into view — its tab
// opened, the browser tab brought forward — would otherwise show what it had
// when it was left until its next tick, so it is asked straight away, by that
// same `quasar:seen` event.
(function () {
  function seen(el) {
    if (document.hidden) return false;
    if (typeof el.checkVisibility === "function") return el.checkVisibility();
    return el.offsetParent !== null || getComputedStyle(el).position === "fixed";
  }

  var POLLED = '[hx-trigger*="quasar:seen"]';
  var wasSeen = new WeakMap();

  document.addEventListener("htmx:beforeRequest", function (e) {
    var elt = e.detail.elt;
    if (elt && elt.matches && elt.matches(POLLED) && !seen(elt)) {
      e.preventDefault();
      wasSeen.set(elt, false);
    }
  });

  // Every polled panel that has just come into view since this last looked.
  function catchUp() {
    document.querySelectorAll(POLLED).forEach(function (el) {
      var now = seen(el);
      if (now && wasSeen.get(el) === false) htmx.trigger(el, "quasar:seen");
      wasSeen.set(el, now);
    });
  }
  function note() {
    document.querySelectorAll(POLLED).forEach(function (el) { wasSeen.set(el, seen(el)); });
  }

  // A poll that brings back exactly what it brought last time changes
  // nothing, and is not swapped in. Swapping it anyway rebuilt the panel every
  // few seconds for nothing: the row under the pointer lost its hover, the
  // button with the focus lost it, and text being selected was unselected.
  //
  // Kept by the address polled rather than by element: a panel that swaps its
  // own outer HTML is a new element after every swap, and the same panel.
  var lastAnswer = new Map();
  document.addEventListener("htmx:beforeSwap", function (e) {
    var elt = e.detail.elt;
    if (!elt || !elt.matches || !elt.matches(POLLED)) return;
    if (!e.detail.requestConfig || e.detail.requestConfig.verb !== "get") return;
    var key = elt.getAttribute("hx-get");
    var text = e.detail.serverResponse;
    if (lastAnswer.get(key) === text) e.detail.shouldSwap = false;
    else lastAnswer.set(key, text);
  });
  // Something other than the poll wrote into the panel — a button answering
  // into it — so what is on screen is no longer the poll's last answer, and
  // the next one is swapped in whatever it says.
  // (A panel replacing its own outer HTML is a new element answering for the
  // old one, so it is told apart by the address it polls, not by identity.)
  document.addEventListener("htmx:afterSwap", function (e) {
    var target = e.detail.target, elt = e.detail.elt;
    if (!target || !target.getAttribute) return;
    var key = target.getAttribute("hx-get");
    if (!elt || !elt.getAttribute || elt.getAttribute("hx-get") !== key) lastAnswer.delete(key);
  });

  document.addEventListener("visibilitychange", catchUp);
  // A tab switch is a click that shows and hides panes; it has run by the
  // time the next frame is drawn.
  document.addEventListener("click", function () { requestAnimationFrame(catchUp); });
  document.addEventListener("htmx:load", note);
  note();
})();

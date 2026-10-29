// Polls only run where somebody can see them.
//
// Every panel that refreshes itself carries `every Ns [quasarSeen(this)]`:
// htmx asks this on each tick and skips the tick on a no. A browser tab in the
// background, or a tab of the application page that is not open, has nobody
// reading it — and was costing the server a container listing, a stats read
// and a database query every few seconds all the same.
//
// The other half is catching up. A panel that comes back into view — its tab
// opened, the browser tab brought forward — would otherwise show what it had
// when it was left until its next tick, so it is asked straight away, by the
// `quasar:seen` event every polled panel also listens for.
(function () {
  function seen(el) {
    if (document.hidden) return false;
    if (typeof el.checkVisibility === "function") return el.checkVisibility();
    return el.offsetParent !== null || getComputedStyle(el).position === "fixed";
  }
  window.quasarSeen = seen;

  var POLLED = '[hx-trigger*="quasarSeen"]';
  var wasSeen = new WeakMap();

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

  document.addEventListener("visibilitychange", catchUp);
  // A tab switch is a click that shows and hides panes; it has run by the
  // time the next frame is drawn.
  document.addEventListener("click", function () { requestAnimationFrame(catchUp); });
  document.addEventListener("htmx:load", note);
  note();
})();

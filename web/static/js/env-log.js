// Keeps the server-update log on its newest line while a job runs.
//
// The Environment card re-fetches itself every two seconds and is replaced
// whole, so the log arrives each time as a new element scrolled to its top.
// Where the reader was is read just before each swap and put back just after:
// at the end, the log follows what is being written; scrolled up, it stays on
// the line they were reading, as the other log panes do.
//
// Loaded once by the System page, not by the card, for the same reason as
// sweep.js: the card is a partial htmx swaps in.
(function () {
  var follow = true;
  var top = 0;

  function pane() { return document.querySelector("#system-env .env-log"); }

  function place() {
    var p = pane();
    if (p) p.scrollTop = follow ? p.scrollHeight : top;
  }

  document.addEventListener("htmx:beforeSwap", function () {
    var p = pane();
    // A log folded away has no position to keep; it opens at its end.
    if (!p || !p.clientHeight) { follow = true; return; }
    follow = p.scrollHeight - p.scrollTop - p.clientHeight < 32;
    top = p.scrollTop;
  });
  document.addEventListener("htmx:afterSettle", place);
  // toggle does not bubble, so it is caught on its way down.
  document.addEventListener("toggle", function (e) {
    if (e.target.open && e.target.querySelector && e.target.querySelector(".env-log")) {
      follow = true;
      place();
    }
  }, true);
})();

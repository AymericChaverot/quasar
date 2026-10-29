(function () {
  var pane = document.getElementById("log-pane");
  var jump = document.getElementById("log-tail");
  if (!pane || !jump) return;

  var follow = true;
  function tail() { pane.scrollTop = pane.scrollHeight; }
  // A few pixels of slack: a pane scrolled by a fractional pixel, or one whose
  // last line is still being laid out, is still "at the bottom" to a reader.
  function sync() {
    follow = pane.scrollHeight - pane.scrollTop - pane.clientHeight < 32;
    jump.hidden = follow;
  }

  pane.addEventListener("scroll", sync);
  jump.addEventListener("click", tail);
  new MutationObserver(function () { if (follow) tail(); }).observe(pane, { childList: true });
  // The stream opens the first time the pane is on screen, not with the page.
  var wrap = pane.closest("[data-sse-connect]");
  function connect() {
    if (!wrap || wrap.hasAttribute("sse-connect") || !pane.offsetParent) return;
    wrap.setAttribute("sse-connect", wrap.dataset.sseConnect);
    htmx.process(wrap);
  }

  // A pane in a closed tab is not laid out, so it has no size until its tab
  // opens — which is the moment to start the stream, and to take the pane to
  // the end of whatever has arrived.
  new ResizeObserver(function () {
    connect();
    if (follow) tail();
  }).observe(pane);
  connect();
  tail();
})();

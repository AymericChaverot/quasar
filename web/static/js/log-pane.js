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
  // A pane in a closed tab is not laid out, so the lines that arrive meanwhile
  // scroll nothing and it opens at the top. It is resized from nothing when
  // its tab opens, which is when it can be taken to the end.
  new ResizeObserver(function () { if (follow) tail(); }).observe(pane);
  tail();
})();

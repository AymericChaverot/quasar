// With an address in the field there is one obvious next thing to do, and the
// button says so by becoming the primary one. Empty, it is a button beside
// another button and neither has any business shouting. The two classes are
// swapped, not blended, so the movement between them is a transition on
// .station-go rather than anything this has to time.
(function () {
  const form = document.getElementById("station-import");
  if (!form) return;
  const url = form.querySelector('input[name="source_url"]');
  const go = form.querySelector('button[type="submit"]');
  function sync() {
    const ready = url.value.trim() !== "";
    go.classList.toggle("btn-primary", ready);
    go.classList.toggle("btn", !ready);
  }
  url.addEventListener("input", sync);
  sync();
})();

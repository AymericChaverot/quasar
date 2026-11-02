// Top consumers: the dashboard's modal of which applications the server goes
// to. It loads its content when it opens, with the window the history above it
// is set to, and again whenever it is opened after that window has changed.
(function () {
  const dialog = document.getElementById("top-consumers");
  const open = document.getElementById("top-consumers-open");
  const body = document.getElementById("top-consumers-body");
  if (!dialog || !open || !body) return;

  // showModal rather than the open attribute: the backdrop, the inert page
  // behind and the escape key all come with it.
  open.addEventListener("click", function () {
    dialog.showModal();
    htmx.trigger(body, "quasar:open");
  });
  document.getElementById("top-consumers-close").addEventListener("click", function () {
    dialog.close();
  });
  // A click on the backdrop lands on the dialog itself, since its bands pad
  // themselves and the panel has none.
  dialog.addEventListener("click", function (e) {
    if (e.target === dialog) dialog.close();
  });
})();

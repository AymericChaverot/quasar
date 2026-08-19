(function () {
  var dialog = document.getElementById("file-modal");
  if (!dialog) return;

  // showModal rather than the open attribute: it is what puts the listing
  // behind inert, draws the backdrop, and makes escape close this.
  //
  // Opening happens after the swap rather than on the click, so a file that
  // takes a moment to read never shows an empty panel — the dialog appears
  // already holding the file.
  dialog.addEventListener("htmx:afterSwap", function () {
    if (!dialog.open) dialog.showModal();
  });

  // The close button arrives with the content, so the listener is on the
  // dialog rather than on a button that does not exist yet.
  dialog.addEventListener("click", function (e) {
    // A click landing on the dialog itself is the backdrop: the panel fills
    // it, so nothing else reaches this.
    if (e.target === dialog || e.target.closest("[data-close-modal]")) dialog.close();
  });

  // htmx restores this page from its history cache on Back, dialog markup
  // included — and a dialog whose content was cached mid-open comes back
  // holding a file while the listing behind it says otherwise.
  dialog.addEventListener("close", function () {
    dialog.innerHTML = "";
  });

  // Deleting the file the dialog is showing leaves it describing something
  // that no longer exists, so the response that did the deleting says to
  // close. The listing behind it refreshes on its own event.
  document.body.addEventListener("quasar:close-modal", function () {
    if (dialog.open) dialog.close();
  });
})();

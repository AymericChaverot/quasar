// The restore button says whether a key came with it.
//
// A key file is only needed for an archive from another server, so the button
// reads "Restore" until one is chosen — at which point saying so is the only
// confirmation that the hidden file input took anything.
//
// Delegated from the document: the backup table is rendered with the page, but
// this keeps working if it ever becomes a partial that is swapped in.
document.addEventListener("change", function (e) {
  var input = e.target;
  if (!(input instanceof HTMLInputElement) || input.name !== "master_key") return;
  var button = input.form && input.form.querySelector("[data-restore]");
  if (button) button.textContent = input.files.length ? "Restore with key" : "Restore";
});

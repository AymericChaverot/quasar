// One confirmation for every destructive form on the site.
//
// The question used to live in an onsubmit attribute, which meant a sentence
// written by the server sat inside a JavaScript string inside an HTML
// attribute — three nested escapes for what is a line of prose. It is a
// data-confirm attribute now: ordinary text, escaped once, and the same
// listener asks it everywhere.
//
// Capturing, so the question is put before any handler that would act on the
// submit; and preventDefault rather than `return false`, so a form that was
// already stopped for another reason stays stopped.
document.addEventListener("submit", function (e) {
  var message = e.target instanceof HTMLFormElement && e.target.dataset.confirm;
  if (message && !confirm(message)) e.preventDefault();
}, true);

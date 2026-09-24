// One confirmation for every destructive action on the site, in the page's
// own dialog rather than the browser's: a native confirm() is drawn by the
// browser, looks like nothing else here, and freezes the page while it is up.
//
// Two things ask. A plain form carries its question in data-confirm — ordinary
// text, escaped once — and its submit is held here until the answer. An htmx
// control carries it in hx-confirm, and htmx raises htmx:confirm before the
// request; that is taken over the same way, and the request issued on a yes.
//
// The dialog borrows its words from what asked: the heading and the button
// that goes ahead say what the clicked button said ("Delete", "Redeploy"), and
// a button drawn as dangerous turns the whole dialog red and says the action
// cannot be taken back. For those, Cancel has the focus, so an Enter pressed
// out of habit does nothing.
(function () {
  var dialog = document.getElementById("confirm-dialog");
  if (!dialog || typeof dialog.showModal !== "function") return;

  var title = document.getElementById("confirm-title");
  var message = document.getElementById("confirm-message");
  var ok = document.getElementById("confirm-ok");
  var cancelButton = dialog.querySelector(".modal-foot [data-confirm-cancel]");
  var proceed = null;

  // What the clicked control says, if it is short enough to be a verb.
  function labelOf(el) {
    var text = el ? el.textContent.replace(/\s+/g, " ").trim() : "";
    return text && text.length <= 32 ? text : "";
  }

  function ask(question, trigger, onYes) {
    var label = labelOf(trigger);
    var danger = !!(trigger && trigger.classList.contains("btn-danger"));
    title.textContent = label ? label + "?" : "Confirm";
    message.textContent = question;
    ok.textContent = label || "Confirm";
    ok.className = danger ? "btn-danger-solid" : "btn-primary";
    dialog.classList.toggle("is-danger", danger);
    proceed = onYes;
    dialog.showModal();
    (danger ? cancelButton : ok).focus();
  }

  function answer(yes) {
    var go = proceed;
    proceed = null;
    dialog.close();
    if (yes && go) go();
  }

  ok.addEventListener("click", function () { answer(true); });
  dialog.querySelectorAll("[data-confirm-cancel]").forEach(function (b) {
    b.addEventListener("click", function () { answer(false); });
  });
  // Escape closes a dialog on its own; this is so it also counts as a no.
  dialog.addEventListener("close", function () { proceed = null; });
  // A click on the backdrop lands on the dialog itself, outside its panel.
  dialog.addEventListener("click", function (e) {
    if (e.target === dialog) answer(false);
  });

  // Plain forms. Capturing, so the question is put before any handler that
  // would act on the submit. The form is sent again on a yes, with the same
  // submitter, and let through that one time.
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!(form instanceof HTMLFormElement) || !form.dataset.confirm) return;
    if (form.dataset.confirmed) {
      delete form.dataset.confirmed;
      return;
    }
    e.preventDefault();
    e.stopImmediatePropagation();
    var submitter = e.submitter || form.querySelector('[type="submit"]');
    ask(form.dataset.confirm, submitter, function () {
      form.dataset.confirmed = "1";
      if (submitter && submitter.form === form) form.requestSubmit(submitter);
      else form.requestSubmit();
    });
  }, true);

  // htmx controls. Every request raises htmx:confirm; only those with a
  // question are held.
  document.addEventListener("htmx:confirm", function (e) {
    var question = e.detail.question;
    if (!question) return;
    e.preventDefault();
    var elt = e.detail.elt;
    var trigger = elt;
    if (elt instanceof HTMLFormElement) {
      var ev = e.detail.triggeringEvent;
      trigger = (ev && ev.submitter) || elt.querySelector('[type="submit"]');
    }
    ask(question, trigger, function () { e.detail.issueRequest(true); });
  });
})();

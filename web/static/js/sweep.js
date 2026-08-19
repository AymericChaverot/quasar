// Keeps the cleanup button honest about what it is about to do.
//
// A control still offering to clean up 3 GB while two lines are ticked would be
// lying about the one thing the card exists to make checkable.
//
// Everything it needs is in the markup — the counts are the server's, not
// re-derived here — so with scripting off the form still submits and the server
// still reads the ticks; only the label stays at its widest, which is the
// reading that was already there.
//
// Loaded once by the System page, not by the card. The card is a partial htmx
// swaps in, so this binds again after every swap rather than shipping a copy of
// itself with each one.
(function () {
  function wire() {
    // One such form per page: this card is the only thing that sweeps. The
    // marker is what makes this safe to call after every swap — htmx fires
    // afterSwap for each of the System page's polled panels, and only a card
    // that was actually replaced arrives without one.
    var form = document.querySelector("form[data-sweep]");
    if (!form || form.dataset.sweepWired) return;
    form.dataset.sweepWired = "1";
    var label = form.querySelector("[data-sweep-label]");
    var scope = form.querySelector("[data-sweep-scope]");
    var button = form.querySelector('button[type="submit"]');
    var volumes = form.querySelector('input[name="volumes"]');
    var whole = { label: label.textContent, scope: scope.textContent };
    var busy = false;

    // How many objects the current ticks add up to. A category ticked whole
    // stands for all of its lines, so its own lines are not counted again.
    function selected() {
      var n = 0;
      form.querySelectorAll(".sweep-group").forEach(function (group) {
        var all = group.querySelector(".is-all input");
        if (!all) return;
        var c = 0;
        if (all.checked) {
          c = Number(all.dataset.count);
        } else {
          group.querySelectorAll(".sweep-object:not(.is-all) input:checked").forEach(function (box) {
            c += Number(box.dataset.count);
          });
        }
        // The volumes consent above the list means all of them, whatever
        // the lines below it say.
        if (volumes && volumes.checked && all.value.indexOf("volumes:") === 0) {
          c = Number(all.dataset.count);
        }
        n += c;
      });
      return n;
    }

    function update() {
      if (busy) return;
      var n = selected();
      if (n === 0) {
        label.textContent = whole.label;
        scope.textContent = whole.scope;
        form.dataset.confirm = "";
        return;
      }
      var objects = n + (n === 1 ? " object" : " objects");
      label.textContent = "Clean up " + objects;
      scope.textContent = "Ticked: " + objects + " — nothing else is touched";
      form.dataset.confirm = "Remove the " + objects + " you ticked? Nothing else is touched.";
    }

    // A sweep of a few gigabytes is minutes of the daemon deleting layers,
    // and this form posts and waits for the redirect rather than going
    // through htmx — so until the new page arrives the browser shows the
    // card exactly as it was, and a button that still reads "Clean up 3 GB"
    // is the one thing telling the operator nothing happened. It says what
    // it is doing instead, and stops taking clicks that would each start
    // another sweep.
    //
    // The state is put up after the event has been let through, not
    // instead of it: the ticks are still enabled when the browser builds
    // what it posts, and are only frozen a task later, once it has.
    function working() {
      button.classList.add("is-working");
      button.disabled = true;
      label.textContent = "Cleaning up…";
      scope.textContent = "Removing what you asked for — this can take a few minutes on a large sweep.";
      form.querySelectorAll("input[type=checkbox]").forEach(function (box) {
        box.disabled = true;
      });
    }

    form.addEventListener("submit", function (e) {
      // The confirm above may have said no, in which case nothing is
      // running and the button must stay as it was.
      if (e.defaultPrevented) return;
      if (busy) {
        e.preventDefault();
        return;
      }
      busy = true;
      setTimeout(working, 0);
    });

    // Coming back to this page from the browser's history hands back the
    // form as it was left — mid-sweep, if that is where it was left. The
    // sweep it was showing is long over by then.
    window.addEventListener("pageshow", function onShow(e) {
      // One listener per card, and the card is replaced on every poll of the
      // panel, so one left over from a card no longer on the page takes itself
      // off rather than piling up.
      if (!form.isConnected) {
        window.removeEventListener("pageshow", onShow);
        return;
      }
      if (!e.persisted || !busy) return;
      busy = false;
      button.classList.remove("is-working");
      button.disabled = false;
      form.querySelectorAll("input[type=checkbox]").forEach(function (box) {
        box.disabled = false;
      });
      update();
    });

    form.addEventListener("change", update);
    update();
  }

  wire();
  document.addEventListener("htmx:afterSwap", wire);
})();

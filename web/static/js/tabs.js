// The tab bar on an application's page.
//
// The panes are all in the page: this is showing and hiding, not fetching, so
// a tab opens with nothing to wait for and everything inside it — the polls,
// the log stream, a form half filled in — carries on across a switch rather
// than being thrown away and asked for again.
//
// The station block further up has a strip of its own, and it is not this one.
// Its tabs answer to events an action can raise ("take me to the Mods tab"),
// which is a station's business and nothing the page needs; keeping the two
// apart is what stops a station reaching the page's own navigation.
//
// Delegated from the document, so a strip that arrives in an htmx swap works
// without being wired up again.
(function () {
  function open(strip, id) {
    for (const tab of strip.querySelectorAll(".page-tab")) {
      const on = tab.dataset.tab === id;
      tab.classList.toggle("is-on", on);
      tab.setAttribute("aria-selected", on);
    }
    for (const pane of document.querySelectorAll(".page-pane")) {
      pane.classList.toggle("hidden", pane.dataset.pane !== id);
    }
  }

  // Only a tab this page actually draws. A viewer has no Environment tab, and
  // a link or a stale bookmark naming one must not blank the page by hiding
  // every pane in favour of one that is not there.
  function known(id) {
    return !!id && !!document.querySelector('.page-tab[data-tab="' + CSS.escape(id) + '"]');
  }

  document.addEventListener("click", function (e) {
    const tab = e.target.closest && e.target.closest(".page-tab");
    if (!tab) return;
    open(tab.closest(".page-tabs"), tab.dataset.tab);
    // In the address bar, so the tab survives a reload and can be linked to.
    // replaceState rather than a hash assignment: setting location.hash also
    // scrolls the page to whatever it names, and here it names nothing.
    history.replaceState(null, "", "#" + tab.dataset.tab);
  });

  function fromHash() {
    const id = location.hash.slice(1);
    const strip = document.querySelector(".page-tabs");
    if (strip && known(id)) open(strip, id);
  }

  fromHash();
  addEventListener("hashchange", fromHash);
})();

// The gate in front of the advanced tabs, on an application deployed from a
// station.
//
// The station's own tab is the page for somebody who installed a station: they
// wanted its controls, and the six tabs behind this button are the platform's
// controls for every application on the server — the compose file, the routing,
// the certificate. Nothing here hides anything; the tabs are in the page, they
// are simply not on the strip until somebody has been told what they are and
// said yes.
//
// The answer is kept in the browser rather than in the database, and against
// this one application. It is a preference about how a page is read, held by
// the person reading it: it belongs to a reader and a screen, not to the
// application, and nothing on the server needs to know it. Cleared storage
// means the question is asked once more, which is the right failure.
(function () {
  const unlock = document.getElementById("advanced-unlock");
  const dialog = document.getElementById("advanced-dialog");
  if (!unlock || !dialog) return;

  const key = "quasar.advanced." + unlock.dataset.app;

  function reveal() {
    for (const el of document.querySelectorAll(".page-tab.is-advanced")) {
      el.classList.remove("hidden");
    }
    unlock.remove();
  }

  // A browser with storage turned off is a browser that asks every time, which
  // is worth far more than a page that will not open because a read threw.
  function remembered() {
    try {
      return localStorage.getItem(key) === "1";
    } catch (e) {
      return false;
    }
  }

  function remember() {
    try {
      localStorage.setItem(key, "1");
    } catch (e) {
      /* asked again next time, and nothing worse */
    }
  }

  if (remembered()) {
    reveal();
  } else {
    unlock.addEventListener("click", () => dialog.showModal());
    for (const id of ["advanced-cancel", "advanced-cancel-x"]) {
      const el = document.getElementById(id);
      if (el) el.addEventListener("click", () => dialog.close());
    }
    document.getElementById("advanced-confirm").addEventListener("click", function () {
      remember();
      reveal();
      dialog.close();
    });
  }
})();

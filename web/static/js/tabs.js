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

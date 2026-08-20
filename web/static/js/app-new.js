(function () {
  const dialog = document.getElementById("catalog-modal");
  const box = document.getElementById("catalog-search");
  const list = document.getElementById("catalog");
  const cards = [...document.querySelectorAll(".catalog-card")];
  const groups = [...document.querySelectorAll(".catalog-group")];
  const empty = document.getElementById("catalog-empty");
  if (!dialog || !box) return;

  // The catalogue being shown: "" for the entries Quasar ships, otherwise the
  // name of one of the operator's. Every button on the page opens this same
  // dialog and only changes what it is looking at.
  let source = "";
  const title = document.getElementById("catalog-modal-title");

  // showModal rather than show: it is what puts the page behind inert, draws
  // the backdrop, and makes the escape key close this — none of which the
  // open attribute alone does.
  for (const button of document.querySelectorAll(".catalog-open")) {
    button.addEventListener("click", function () {
      source = button.dataset.source;
      title.textContent = button.dataset.label;
      // A category picked in one catalogue means nothing in the next, and
      // leaving it on would open the dialog looking empty.
      pickCategory("");
      box.value = "";
      showList();
      filter();
      dialog.showModal();
    });
  }
  document.getElementById("catalog-close").addEventListener("click", function () {
    dialog.close();
  });
  // A click that lands on the backdrop is reported against the dialog itself,
  // since the panel has no padding for it to land in — so this is the click
  // outside, and nothing else reaches it.
  dialog.addEventListener("click", function (e) {
    if (e.target === dialog) dialog.close();
  });

  // Filtering happens here rather than over the wire: the whole catalogue is
  // already on the page, and a round trip per keystroke would be slower than
  // the typing.
  //
  // The two filters are one function because they compose: a category picked
  // and a word typed means both, not whichever ran last.
  let category = "";
  function filter() {
    const q = box.value.trim().toLowerCase();
    for (const c of cards) {
      const hit = c.dataset.source === source &&
                  (q === "" || c.dataset.search.toLowerCase().includes(q)) &&
                  (category === "" || c.dataset.cat === category);
      c.classList.toggle("hidden", !hit);
    }
    // A category whose every card is hidden should take its heading with it.
    for (const g of groups) {
      g.classList.toggle("hidden", !g.querySelector(".catalog-card:not(.hidden)"));
    }
    // And a category this catalogue has nothing in should not be offered as
    // a filter — the row is otherwise mostly chips that empty the list.
    // Measured against the source alone, so a chip does not disappear from
    // under the pointer as the search narrows what is left.
    for (const chip of chips) {
      chip.classList.toggle("hidden", chip.dataset.cat !== "" &&
        !cards.some(c => c.dataset.source === source && c.dataset.cat === chip.dataset.cat));
    }
    empty.classList.toggle("hidden", !!document.querySelector(".catalog-card:not(.hidden)"));
  }
  box.addEventListener("input", filter);

  const chips = [...document.querySelectorAll(".chip-filter")];
  function pickCategory(next) {
    category = next;
    for (const chip of chips) {
      const on = chip.dataset.cat === category;
      chip.classList.toggle("is-on", on);
      chip.setAttribute("aria-pressed", on);
    }
  }
  for (const chip of chips) {
    chip.addEventListener("click", function () {
      // Clicking the category already showing goes back to all of them,
      // so the row never traps you with no way out but the All chip.
      pickCategory(chip.dataset.cat === category ? "" : chip.dataset.cat);
      filter();
    });
  }

  // An entry that asks something opens its panel instead of navigating: the
  // answers belong in the address, so the request is made once they are in.
  const panels = [...document.querySelectorAll(".catalog-params")];
  function showList() {
    for (const p of panels) p.classList.add("hidden");
    list.classList.remove("hidden");
    box.parentElement.classList.remove("hidden");
    box.focus();
  }
  for (const c of cards) {
    if (!c.dataset.params) continue;
    c.addEventListener("click", function (e) {
      e.preventDefault();
      const panel = document.getElementById(c.dataset.params);
      if (!panel) return;
      list.classList.add("hidden");
      // The search box and the category row filter the list that is no
      // longer showing, so they go with it.
      box.parentElement.classList.add("hidden");
      panel.classList.remove("hidden");
      const first = panel.querySelector("input:not([type=hidden]), select");
      if (first) first.focus();
    });
  }
  for (const b of document.querySelectorAll(".catalog-params-back")) {
    b.addEventListener("click", showList);
  }
  // Reopening the catalogue should show the catalogue, not the panel that
  // happened to be open when it was closed.
  dialog.addEventListener("close", showList);
})();

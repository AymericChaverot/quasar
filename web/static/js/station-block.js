(function () {
  const block = document.getElementById("station");
  if (!block) return;

  function show(id) {
    for (const tab of block.querySelectorAll(".station-tab")) {
      const on = tab.dataset.stationTab === id;
      tab.classList.toggle("is-on", on);
      tab.setAttribute("aria-selected", on);
    }
    for (const pane of block.querySelectorAll(".station-pane")) {
      pane.classList.toggle("hidden", pane.dataset.stationPane !== id);
    }
  }
  for (const tab of block.querySelectorAll(".station-tab")) {
    tab.addEventListener("click", () => show(tab.dataset.stationTab));
  }
  // An action may ask to be taken to a tab, which arrives as an event named
  // after it rather than as anything the script got to write.
  for (const pane of block.querySelectorAll(".station-pane")) {
    const id = pane.dataset.stationPane;
    document.body.addEventListener("quasar:station-tab-" + id, () => show(id));
  }

  // The refresh button asks every panel to fetch itself again, by name of an
  // event rather than by a list: panels come and go as tabs and grids draw,
  // and a button holding a list of them would be wrong within a version.
  const refresh = block.querySelector(".station-refresh");
  if (refresh) {
    refresh.addEventListener("click", () => {
      refresh.classList.add("is-turning");
      setTimeout(() => refresh.classList.remove("is-turning"), 620);
      document.body.dispatchEvent(new CustomEvent("quasar:station-refresh"));
    });
  }

  // Each message that lands is given its lifetime once. Arming happens here
  // rather than in a script travelling with the message, because a fragment
  // that is appended rather than swapped would leave one dead script tag in
  // the container per button anybody ever pressed.
  const toasts = document.getElementById("station-message");
  const MAX = 5;
  function arm() {
    const all = toasts.querySelectorAll(".station-toast");
    // A run of failures should not end up a column of cards down the whole
    // screen; the oldest go, since the newest is what was just pressed.
    for (let i = 0; i < all.length - MAX; i++) all[i].remove();

    for (const toast of toasts.querySelectorAll(".station-toast:not([data-armed])")) {
      toast.dataset.armed = "1";
      // An error has no lifetime: why something failed is what somebody came
      // to read, and it stays until they dismiss it.
      const life = Number(toast.dataset.toastLife || 0);
      if (!life) continue;
      setTimeout(() => {
        toast.classList.add("is-going");
        setTimeout(() => toast.remove(), 400);
      }, life);
    }
  }
  document.body.addEventListener("htmx:afterSwap", (e) => {
    if (e.target === toasts) arm();
  });
})();

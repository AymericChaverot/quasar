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

  // An action may hand over a file, which arrives as an address in the event
  // rather than as anything on the page: the response to a button press is a
  // toast, and a toast cannot also be a download. The anchor is clicked instead
  // of setting location, so a browser that decides to render the file rather
  // than save it still leaves this page where it was.
  document.body.addEventListener("quasar:station-download", (e) => {
    const url = e.detail && e.detail.url;
    if (!url) return;
    const link = document.createElement("a");
    link.href = url;
    link.download = "";
    document.body.appendChild(link);
    link.click();
    link.remove();
  });
})();

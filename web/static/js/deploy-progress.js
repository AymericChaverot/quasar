(function () {
  var panel = document.getElementById("deploy-panel");
  if (!panel) return;
  var phaseEl = document.getElementById("deploy-phase");
  var pctEl = document.getElementById("deploy-percent");
  var meter = document.getElementById("deploy-meter");
  var bar = meter.firstElementChild;
  var errEl = document.getElementById("deploy-error");
  var output = document.getElementById("deploy-output");
  var pane = document.getElementById("deploy-log");
  var jump = document.getElementById("deploy-tail");
  var toggle = document.getElementById("deploy-toggle");

  // The pane follows the newest line until the reader scrolls up, and stops
  // until they ask to come back: being yanked to the tail mid-read is worse
  // than falling behind.
  var follow = true;
  function tail() { pane.scrollTop = pane.scrollHeight; }
  function sync() {
    follow = pane.scrollHeight - pane.scrollTop - pane.clientHeight < 32;
    jump.hidden = follow;
  }
  pane.addEventListener("scroll", sync);
  jump.addEventListener("click", tail);
  new MutationObserver(function () { if (follow) tail(); }).observe(pane, { childList: true });

  toggle.addEventListener("click", function () {
    output.hidden = !output.hidden;
    toggle.textContent = output.hidden ? "Show output" : "Hide output";
    if (!output.hidden) tail();
  });

  var src = new EventSource(panel.dataset.stream);

  src.addEventListener("reset", function () { pane.replaceChildren(); });

  src.addEventListener("line", function (e) {
    var row = document.createElement("div");
    row.innerHTML = e.data;
    pane.append(row.firstElementChild || row);
  });

  var wasRunning = null;

  src.addEventListener("state", function (e) {
    var s = JSON.parse(e.data);
    panel.hidden = !s.active;
    if (!s.active) return;

    // The output opens for a deploy that is running and stays shut for one that
    // has already ended — a page opened hours later is about the application,
    // not about the build that put it there — but it is still one click away,
    // and a deploy starting while the page is open opens it.
    if (wasRunning !== s.running) {
      if (wasRunning === null || s.running) {
        output.hidden = !s.running;
        toggle.textContent = s.running ? "Hide output" : "Show output";
      }
      wasRunning = s.running;
      if (s.running) tail();
    }

    // A percentage nothing is feeding is shown as movement rather than as a
    // number that has stopped: the width still says how much of the deploy is
    // behind it, and the stripe says the step it is in has no count to give.
    bar.style.width = Math.min(100, Math.max(0, s.percent)) + "%";
    meter.classList.toggle("is-waiting", s.running && !s.measured);
    pctEl.textContent = s.measured || !s.running ? Math.round(s.percent) + "%" : "";

    errEl.hidden = !s.error;
    errEl.textContent = s.error || "";
    if (s.running) {
      phaseEl.textContent = s.phase || "Starting…";
    } else if (s.error) {
      phaseEl.textContent = "The deploy stopped.";
    } else {
      phaseEl.textContent = "Deployed.";
    }
    // The phase names what is happening while it is happening; once the deploy
    // has ended the same line is a verdict, and a verdict must sit still.
    phaseEl.classList.toggle("shimmer", !!s.running);
    bar.classList.toggle("fill-info", !s.error);
    bar.classList.toggle("fill-err", !!s.error);
  });
})();

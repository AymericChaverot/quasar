// The toasts in the corner of every page.
//
// They arrive three ways: drawn with the page (the answer to the form that led
// here), appended by a button on the page (hx-target="#toasts"), and over the
// stream every page holds open, for something this person started that has
// ended while they were elsewhere. All three land in the same container, and
// whatever lands there is armed once, here, rather than by a script travelling
// with each toast.
(function () {
  const box = document.getElementById("toasts");
  if (!box) return;
  const MAX = 5;

  // A toast's clock only runs while somebody could be reading it: not while
  // it is pointed at or focused, and not while the tab is out of sight. A
  // success that came back from a ten-minute job while you were in another
  // tab has not been seen just because six seconds went by.
  function held(t) {
    return document.hidden || t.matches(":hover") || t.contains(document.activeElement);
  }

  function dismiss(t) {
    if (t.classList.contains("is-going")) return;
    if (t.clock) t.clock.stop();
    t.classList.add("is-going");
    setTimeout(() => t.remove(), 300);
  }

  function arm(t) {
    if (t.dataset.armed) return;
    t.dataset.armed = "1";

    // A toast that names a notice takes the place of the one already showing
    // it: "started" becoming "finished" is one toast that changed.
    const id = t.dataset.notice;
    if (id) {
      for (const old of box.querySelectorAll(".toast[data-notice]")) {
        if (old !== t && old.dataset.notice === id) old.remove();
      }
    }

    const life = Number(t.dataset.life || 0);
    if (!life) return;
    t.style.setProperty("--life", life + "ms");
    let left = life;
    let since = 0;
    let timer = null;
    t.clock = {
      run() {
        if (timer || held(t)) return;
        since = Date.now();
        timer = setTimeout(() => dismiss(t), left);
        t.classList.remove("is-paused");
      },
      stop() {
        if (timer) {
          clearTimeout(timer);
          timer = null;
          left -= Date.now() - since;
        }
        t.classList.add("is-paused");
      },
    };
    t.addEventListener("mouseenter", t.clock.stop);
    t.addEventListener("focusin", t.clock.stop);
    t.addEventListener("mouseleave", t.clock.run);
    t.addEventListener("focusout", () => setTimeout(t.clock.run));
    if (held(t)) t.clock.stop();
    else t.clock.run();
  }

  function settle() {
    const all = box.querySelectorAll(".toast");
    // A run of failures should not become a column of cards up the whole
    // screen. The oldest go, since the newest is what was just pressed.
    for (let i = 0; i < all.length - MAX; i++) all[i].remove();
    for (const t of box.querySelectorAll(".toast:not([data-armed])")) arm(t);
  }

  new MutationObserver(settle).observe(box, { childList: true });
  settle();

  document.addEventListener("visibilitychange", () => {
    for (const t of box.querySelectorAll(".toast")) {
      if (!t.clock) continue;
      if (document.hidden) t.clock.stop();
      else t.clock.run();
    }
  });

  // Delegated, because every toast after the first page arrived after this ran.
  box.addEventListener("click", (e) => {
    const close = e.target.closest(".toast-close");
    if (close) dismiss(close.closest(".toast"));
  });

  if (box.dataset.stream && window.EventSource) {
    const src = new EventSource(box.dataset.stream);
    src.addEventListener("notice", (e) => {
      if (e.data) box.insertAdjacentHTML("beforeend", e.data);
    });
  }
})();

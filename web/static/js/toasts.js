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
    // Gone once it has slid out, or straight away where there is no motion
    // to wait for. The timer is the backstop for an animation that never
    // reports its end, as in a tab the browser has stopped painting.
    let gone = false;
    const go = () => { if (!gone) { gone = true; t.remove(); } };
    t.addEventListener("animationend", (e) => { if (e.target === t) go(); });
    setTimeout(go, 400);
  }

  // Where each toast stood the last time the stack settled. When one arrives
  // or leaves, the others are drawn where they were and slid to where they
  // now are, rather than jumping a toast's height in one frame.
  let places = new Map();
  function measure() {
    const next = new Map();
    for (const t of box.children) next.set(t, t.getBoundingClientRect().top);
    return next;
  }
  function glide() {
    const now = measure();
    for (const [t, top] of now) {
      const was = places.get(t);
      if (was === undefined || was === top || t.classList.contains("is-going")) continue;
      t.animate(
        [{ translate: `0 ${was - top}px` }, { translate: "0 0" }],
        { duration: 320, easing: "cubic-bezier(0.16, 1, 0.3, 1)" },
      );
    }
    places = now;
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
    // A toast carried over from the last page comes with what was left of its
    // time, and its hairline starts that far along.
    let left = Math.min(life, Number(t.dataset.left || life));
    t.style.setProperty("--life", life + "ms");
    t.style.setProperty("--elapsed", life - left + "ms");
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
      remaining() {
        return timer ? left - (Date.now() - since) : left;
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
    // Several landing together come in one after the other, the first to
    // arrive first.
    let order = 0;
    for (const t of box.querySelectorAll(".toast:not([data-armed])")) {
      t.style.setProperty("--enter-delay", order++ * 90 + "ms");
      arm(t);
    }
    glide();
  }

  // Toasts outlive the page they were shown on. Whatever is still on screen
  // when a page is left is put back on the next one in this tab, with the
  // time it had left — a "Cleanup started" should not vanish because its
  // page was swapped for the one its form redirected to, and an error stays
  // until it is dismissed, wherever that turns out to be. The tab's own
  // storage, so a second tab does not inherit the first one's.
  const KEPT = "quasar.toasts";
  window.addEventListener("pagehide", () => {
    const keep = [];
    for (const t of box.querySelectorAll(".toast:not(.is-going)")) {
      const left = t.clock ? Math.round(t.clock.remaining()) : 0;
      if (t.clock && left < 500) continue;
      const copy = t.cloneNode(true);
      copy.removeAttribute("data-armed");
      copy.removeAttribute("style");
      copy.classList.remove("is-paused", "is-restored");
      if (t.clock) copy.dataset.left = left;
      keep.push(copy.outerHTML);
    }
    try {
      if (keep.length) sessionStorage.setItem(KEPT, JSON.stringify(keep));
      else sessionStorage.removeItem(KEPT);
    } catch (e) {}
  });
  try {
    const kept = JSON.parse(sessionStorage.getItem(KEPT) || "[]");
    sessionStorage.removeItem(KEPT);
    // Before the ones this page was drawn with, which are newer.
    const tmp = document.createElement("div");
    tmp.innerHTML = kept.join("");
    for (const t of [...tmp.children].reverse()) {
      t.classList.add("is-restored");
      box.prepend(t);
    }
  } catch (e) {}

  new MutationObserver(settle).observe(box, { childList: true });
  settle();
  // A window resized reflows the stack without anything arriving or leaving;
  // the next change should slide from where things are, not from before.
  window.addEventListener("resize", () => { places = measure(); });

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

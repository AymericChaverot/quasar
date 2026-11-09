// Keeping the page where it was across a plain form's round trip.
//
// Most actions on the dashboard are ordinary forms: they post, the server
// answers with the page again (or a redirect to it), and the browser draws it
// from the top. Creating a token at the bottom of Settings then meant
// scrolling back down to read it. So a form that posts records where it sat on
// the screen, and the page that comes back puts it there again.
//
// The form itself is the anchor rather than the scroll offset: what the action
// changed — a token shown, a row added above — moves everything below it, and
// the form is what the person was looking at. When the form is gone (the row
// it revoked), the offset is used instead, on the same page only. Whatever the
// server marks data-reveal (the token, shown once) is then brought into view
// if the restored position left it outside.
//
// htmx forms never leave the page, so they are left alone; so are forms that
// lead somewhere else, since the form is not found there.
(function () {
  var KEY = "quasar.keepScroll";
  var MAX_AGE = 30000;

  function store(v) {
    try { sessionStorage.setItem(KEY, JSON.stringify(v)); } catch (e) {}
  }
  function take() {
    try {
      var v = JSON.parse(sessionStorage.getItem(KEY) || "null");
      sessionStorage.removeItem(KEY);
      return v;
    } catch (e) { return null; }
  }

  function isHtmx(form) {
    return ["hx-post", "hx-put", "hx-patch", "hx-delete", "hx-get"].some(function (a) { return form.hasAttribute(a); });
  }

  // Bubbling, and after the fact: a form held for a confirmation is stopped
  // by confirm.js first, and only recorded when it is really sent.
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (e.defaultPrevented || !(form instanceof HTMLFormElement)) return;
    if ((form.getAttribute("method") || "get").toLowerCase() !== "post" || isHtmx(form) || form.target) return;
    store({
      path: location.pathname,
      action: form.getAttribute("action") || "",
      top: form.getBoundingClientRect().top,
      y: window.scrollY,
      at: Date.now()
    });
  });

  var saved = take();
  if (!saved || Date.now() - saved.at > MAX_AGE) return;

  // The same page, or the page a form under it answered with: a token
  // created from /settings answers at /settings/personal-tokens, and a form
  // posted from there comes back to /settings.
  function samePage() {
    var p = location.pathname, s = saved.path;
    return p === s || p.indexOf(s + "/") === 0 || s.indexOf(p + "/") === 0;
  }

  function anchor() {
    if (!saved.action) return null;
    var forms = document.querySelectorAll("form[method=post], form[method=POST]");
    for (var i = 0; i < forms.length; i++) {
      if (forms[i].getAttribute("action") === saved.action && !isHtmx(forms[i])) return forms[i];
    }
    return null;
  }

  // The user scrolling first wins over anything still to be restored.
  var moved = false;
  function onUserScroll() { moved = true; }
  ["wheel", "touchmove", "keydown"].forEach(function (t) {
    window.addEventListener(t, onUserScroll, { passive: true, once: true });
  });

  function restore() {
    if (moved) return;
    var form = anchor();
    if (form) {
      window.scrollTo(0, window.scrollY + form.getBoundingClientRect().top - saved.top);
    } else if (samePage()) {
      window.scrollTo(0, saved.y);
    } else {
      return;
    }
    var reveal = document.querySelector("[data-reveal]");
    if (reveal) reveal.scrollIntoView({ block: "nearest" });
  }

  // The page is drawn but may still grow (fonts, images) and a #fragment in
  // the address scrolls once loading ends, so this runs again then. Panels
  // htmx fetches after the page — the System page's are — can hold the form
  // or push it down, so it runs again as each arrives, for a few seconds
  // only: after that, a polled panel redrawing must not move the page.
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", restore);
  } else {
    restore();
  }
  window.addEventListener("load", function () { requestAnimationFrame(restore); });
  document.addEventListener("htmx:afterSettle", restore);
  setTimeout(function () { document.removeEventListener("htmx:afterSettle", restore); }, 4000);
})();

// The flourish around a star, in two halves that cannot be written the same
// way. Adding is played afterwards, on the lists the server answered with,
// and holds nothing up: the click posts, the lists come back, the card that
// arrives already carries the state, and the animation runs over the top of
// it. That is why these are keyframes and not transitions — an element that
// has just been inserted has no previous value to transition from.
//
// Removing has to be played first. What moves is the card up in the
// favourites, and the swap is the thing that takes it away; nothing can be
// shown leaving once it has left. So the leaving runs at the click and the
// repaint is held just long enough to land on the state it ends in. What is
// held is the repaint alone — the star itself was stored the moment the
// server answered, and is never waiting on any of this.
//
// Delegated from the document, because the cards are what the swap replaces
// and a listener bound to one would go with it.
(function () {
  const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  const HOLD = 340; // how long a departure is left on screen before the swap
  let pending = null;

  // A starred station is on the page twice — once in the favourites and once
  // in the list of all of them. Both copies are the same station, and they
  // are found by the address they post to, which is the only thing on a card
  // that names which one it is.
  function cards(post, within) {
    return Array.prototype.filter
      .call((within || document).querySelectorAll(".station-star"), function (b) {
        return b.getAttribute("hx-post") === post;
      })
      .map(function (b) { return b.closest(".station-card"); })
      .filter(Boolean);
  }

  // Opening and closing the favourites list: the same fold in two directions.
  // The first favourite brings the whole list with it, heading and all, and
  // the last one takes it away again — either way everything below moves by
  // its height in a single frame unless the height is given to it.
  //
  // The gap between the two lists travels with it, and which element carries
  // that gap has to be asked rather than assumed. It can sit on either side
  // of the seam — a margin under this list, or a margin over the next — and
  // the utility in use here puts it under every child but the last, which
  // means it is this list that carries it, and carries it only from the
  // moment it exists. Reading the wrong side is not a near miss: the
  // animation sets a margin of its own for as long as it runs, so a gap read
  // as nought is a gap held at nought and then handed back all at once on the
  // last frame. Both sides are read; the one that is zero costs nothing.
  function fold(list, open) {
    const next = list.nextElementSibling;
    const own = parseFloat(getComputedStyle(list).marginBottom) || 0;
    const after = next ? parseFloat(getComputedStyle(next).marginTop) || 0 : 0;
    // Safe to read before the clipping goes on, and safe to read after: the
    // list carries its own formatting context at all times, so clipping it
    // cannot change its height. See the note on #station-favorites.
    const height = list.getBoundingClientRect().height;
    list.style.overflow = "hidden";
    const shut = { height: "0px", marginBottom: -after + "px", opacity: 0 };
    const full = { height: height + "px", marginBottom: own + "px", opacity: 1 };
    const run = list.animate(open ? [shut, full] : [full, shut], {
      duration: open ? 420 : HOLD,
      easing: open ? "ease-out" : "ease-in",
      fill: "forwards",
    });
    // Opening hands the element back to the layout, and hands it back in one
    // go: cancelling inside the callback drops the held values in the same
    // frame the overflow is cleared, rather than leaving a frame between them
    // for the page to settle twice. Closing keeps its last frame — what
    // follows is not this element restored but the answer replacing it.
    if (open) {
      run.onfinish = function () {
        run.cancel();
        list.style.overflow = "";
      };
    }
  }

  // Everything a card does to say it has just been starred, and the tidying
  // up afterwards so nothing is left carrying it.
  function play(card, mark) {
    // The glint is a second copy of the name laid over the first, and this is
    // where that copy comes from: the pseudo-element takes its text from the
    // attribute. Set here rather than rendered into every card, because it
    // exists for half a second on one card and is markup nobody else needs.
    const name = card.querySelector(".station-card-name");
    if (name && mark === "is-starring") name.setAttribute("data-glint", name.textContent);
    card.classList.add(mark);
    // A timer rather than the animations' own end events: there are several,
    // they finish at different moments, and the longest of them may not exist
    // at all depending on what the browser can mask.
    setTimeout(function () {
      card.classList.remove(mark, "is-arriving");
      if (name) name.removeAttribute("data-glint");
    }, 800);
  }

  // On the way down rather than on the way up. htmx's own handler sits on the
  // button, so a listener on the document would run after it — and one of the
  // things decided here is the swap attribute htmx is about to act on.
  document.addEventListener("click", function (e) {
    const star = e.target.closest && e.target.closest(".station-star");
    if (!star) return;
    const post = star.getAttribute("hx-post");
    const card = star.closest(".station-card");
    const adding = !(card && card.classList.contains("is-favorite"));
    const list = document.getElementById("station-favorites");
    // Whether there was a favourites list at all a moment ago. The one that
    // comes back is a different element either way, so this is the only place
    // the difference can be seen.
    pending = adding ? { post: post, hadList: !!list } : null;

    // Only a departure is worth waiting for, and only if there is any
    // movement to wait for: with movement turned down there is nothing to
    // see and the lists come back as fast as the server can send them.
    const leaving = !adding && !still && list;
    star.setAttribute("hx-swap", leaving ? "outerHTML swap:" + HOLD + "ms" : "outerHTML");
    if (!leaving) return;

    const going = cards(post, list);
    // The last favourite takes the list with it; any other leaves it standing.
    if (going.length && list.querySelectorAll(".station-card").length === going.length) {
      fold(list, false);
    } else {
      going.forEach(function (c) { c.classList.add("is-leaving"); });
    }
    // And the halo goes from the copy in the list below at the same moment,
    // so both ends of the page are doing the one thing at the one time.
    cards(post).forEach(function (c) {
      if (!list.contains(c)) c.classList.add("is-unstarring");
    });
  }, true);

  document.body.addEventListener("htmx:afterSwap", function () {
    if (!pending) return;
    const done = pending;
    pending = null;
    const list = document.getElementById("station-favorites");
    if (!still && !done.hadList && list && list.animate) fold(list, true);
    cards(done.post).forEach(function (card) {
      // The copy up in the favourites is the one that was not on the page a
      // moment ago; the one in the list below has only changed state.
      if (list && list.contains(card)) card.classList.add("is-arriving");
      play(card, "is-starring");
    });
  });
})();

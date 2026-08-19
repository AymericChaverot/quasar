// Takes the shimmer off its element's clock and puts it on the document's.
//
// A CSS animation belongs to the element running it and starts over when that
// element does — and these words live in panels that are polled: the status
// badge on an application page is thrown away and rebuilt every three seconds,
// the dashboard's table every five. So the sweep restarted on the poll tick,
// and read as an animation stuttering in time with something that has nothing
// to do with it.
//
// Pinning each sweep's start to the timeline origin makes it a function of the
// document clock alone. An element replaced mid-sweep comes back at exactly the
// phase its predecessor had — not near it — and every shimmer on the page runs
// as one. Pinning an already pinned animation changes nothing, so this can run
// after every swap.
(function () {
  // The animations that must survive their element being replaced, and the
  // elements that run them. The update button is in here for the same reason:
  // it is swapped by its own poll every minute, and a halo that started over on
  // each swap would pulse in time with the poll.
  var PINNED = ["shimmer", "update-glow"];

  function pin() {
    document.querySelectorAll(".shimmer, .btn-update").forEach(function (el) {
      // getAnimations reports nothing for an element whose style has not been
      // resolved yet, which a just-inserted one has not, and nothing at all
      // where reduced motion has taken the animation away.
      getComputedStyle(el).animationName;
      el.getAnimations().forEach(function (a) {
        if (PINNED.indexOf(a.animationName) >= 0) a.startTime = 0;
      });
    });
  }

  pin();
  document.addEventListener("htmx:afterSwap", pin);
})();

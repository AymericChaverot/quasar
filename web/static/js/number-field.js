// Number fields get steppers drawn like the rest of the interface. The
// browser's own arrows cannot be restyled — Chrome's are grey system widgets,
// Firefox's something else again — so they are hidden in components.css and
// replaced by two buttons inside the field's right edge.
//
// The buttons use stepUp/stepDown, so min, max and step apply as they do to
// the keyboard's arrows, which keep working; holding one down repeats. They
// are left out of the tab order: the field itself is what a keyboard reaches.
(function () {
  var chevron = function (dir) {
    var b = document.createElement("button");
    b.type = "button";
    b.tabIndex = -1;
    b.className = "num-step num-step-" + dir;
    b.setAttribute("aria-hidden", "true");
    return b;
  };

  function step(input, dir) {
    if (input.disabled || input.readOnly) return;
    // An empty field steps from its minimum, or zero, as the browser's own
    // arrows do.
    if (dir === "up") input.stepUp();
    else input.stepDown();
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function enhance(input) {
    if (input.closest(".num-field")) return;
    var wrap = document.createElement("span");
    // A field sized by a width class keeps that width; one that fills its
    // container still does.
    wrap.className = /(^|\s)w-/.test(input.className) ? "num-field is-inline" : "num-field";
    input.parentNode.insertBefore(wrap, input);
    wrap.appendChild(input);
    var up = chevron("up");
    var down = chevron("down");
    wrap.appendChild(up);
    wrap.appendChild(down);

    [[up, "up"], [down, "down"]].forEach(function (pair) {
      var btn = pair[0], dir = pair[1], delay, repeat;
      var stop = function () {
        clearTimeout(delay);
        clearInterval(repeat);
      };
      btn.addEventListener("pointerdown", function (e) {
        if (e.button !== 0) return;
        e.preventDefault(); // keep the focus where it was
        step(input, dir);
        delay = setTimeout(function () {
          repeat = setInterval(function () { step(input, dir); }, 60);
        }, 400);
      });
      btn.addEventListener("pointerup", stop);
      btn.addEventListener("pointerleave", stop);
      btn.addEventListener("pointercancel", stop);
    });
  }

  function scan(root) {
    (root || document).querySelectorAll('input[type="number"].input').forEach(enhance);
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", function () { scan(); });
  else scan();
  // Forms that htmx swaps in bring their own fields.
  document.addEventListener("htmx:afterSettle", function (e) { scan(e.target); });
})();

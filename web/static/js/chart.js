// Following the pointer across a chart.
//
// Everything a reader is shown was worded by the server and travels in the
// wrapper's data-chart attribute: the moment of every column, where it sits,
// and what each series was worth there. Nothing here decides what a number
// says — it finds the nearest column and moves three things to it, which is
// the whole of the difference between a graph you can look at and one you can
// read.
//
// It replaces the native <title> the sparklines use. That was the right tool
// at 240 by 48: it appears after a second, in the browser's own box, only over
// the exact pixel of a point, and it can say what one series was doing and
// never what the others were.
//
// Delegated from the document, because a panel is what htmx swaps and a
// listener bound to one would go with it. The payload is parsed on the first
// hover rather than at load: a page can hold half a dozen charts and most of
// them are never pointed at.
(function () {
  const PARSED = new WeakMap();

  // The attribute rather than the property, because half of what is shown and
  // hidden here lives in the SVG. `hidden` is defined on HTMLElement and not
  // on SVGElement, so `line.hidden = false` on a <line> quietly sets a
  // property nobody reads and leaves the attribute — and the element — exactly
  // where it was. It fails silently, and the failure looks like a readout
  // appearing beside a crosshair that never arrives.
  function reveal(el, on) {
    if (!el) return;
    if (on) el.removeAttribute("hidden");
    else el.setAttribute("hidden", "");
  }

  function chart(wrap) {
    if (PARSED.has(wrap)) return PARSED.get(wrap);
    let data = null;
    try {
      data = JSON.parse(wrap.dataset.chart || "{}");
    } catch (e) {
      data = null;
    }
    if (!data || !data.x || !data.x.length) data = null;
    const parts = data && {
      data: data,
      svg: wrap.querySelector(".chart"),
      cursor: wrap.querySelector(".chart-cursor"),
      marks: wrap.querySelectorAll(".chart-mark"),
      readout: wrap.querySelector(".chart-readout"),
      when: wrap.querySelector(".chart-when"),
      values: wrap.querySelectorAll(".chart-value"),
      rows: wrap.querySelectorAll(".chart-readout li"),
    };
    PARSED.set(wrap, parts);
    return parts;
  }

  // Which column the pointer is over, in the chart's own coordinates. The
  // columns are in order and evenly spaced far more often than not, but the
  // search is a plain scan for the nearest: a series that stopped for an hour
  // leaves a gap, and a guess from the spacing would read the wrong column
  // either side of it.
  function nearest(xs, x) {
    let best = 0;
    let gap = Infinity;
    for (let i = 0; i < xs.length; i++) {
      const d = Math.abs(xs[i] - x);
      if (d > gap) break; // ordered, so once it turns we are past it
      gap = d;
      best = i;
    }
    return best;
  }

  function show(wrap, clientX) {
    const c = chart(wrap);
    if (!c) return;
    const box = c.svg.getBoundingClientRect();
    if (!box.width) return;

    // Client pixels into viewBox units, which is what every coordinate the
    // server sent is in.
    const x = ((clientX - box.left) / box.width) * c.svg.viewBox.baseVal.width;
    const i = nearest(c.data.x, x);
    const at = c.data.x[i];

    c.cursor.setAttribute("x1", at);
    c.cursor.setAttribute("x2", at);
    reveal(c.cursor, true);

    c.data.series.forEach(function (s, n) {
      const y = s.y[i];
      const mark = c.marks[n];
      const row = c.rows[n];
      const missing = y === null || y === undefined;
      if (mark) {
        reveal(mark, !missing);
        if (!missing) {
          mark.setAttribute("cx", at);
          mark.setAttribute("cy", y);
        }
      }
      // A series with nothing at this column keeps its place in the readout
      // rather than closing the gap, so the rows do not dance as the pointer
      // crosses the moment one of them started.
      if (row) row.classList.toggle("is-absent", missing);
      if (c.values[n]) c.values[n].textContent = s.value[i] || "—";
    });
    c.when.textContent = c.data.at[i];
    reveal(c.readout, true);

    // The readout follows the column and stays inside the card: against the
    // right edge it flips to the other side of the cursor rather than hanging
    // off the end.
    const share = at / c.svg.viewBox.baseVal.width;
    c.readout.classList.toggle("is-left", share > 0.55);
    c.readout.style.left = (share * 100).toFixed(2) + "%";
  }

  function hide(wrap) {
    const c = chart(wrap);
    if (!c) return;
    reveal(c.cursor, false);
    reveal(c.readout, false);
    c.marks.forEach(function (m) { reveal(m, false); });
  }

  document.addEventListener("pointermove", function (e) {
    const wrap = e.target.closest && e.target.closest(".chart-wrap");
    if (wrap) show(wrap, e.clientX);
  });

  // The wrapper itself, and nothing inside it. pointerleave does not bubble,
  // so it is caught on the way down — and on the way down it arrives for every
  // element the pointer leaves, which inside a chart is a polyline or a bar on
  // almost every frame. Asking closest() would then hide the readout as fast
  // as the move above draws it, and the coordinates would be right the whole
  // time with nothing on screen to show for them.
  document.addEventListener("pointerleave", function (e) {
    const wrap = e.target;
    if (wrap.classList && wrap.classList.contains("chart-wrap")) hide(wrap);
  }, true);

  // A chart that scrolls out from under a stationary pointer would otherwise
  // keep its readout where the pointer no longer is.
  document.addEventListener("pointerdown", function (e) {
    document.querySelectorAll(".chart-wrap").forEach(function (wrap) {
      if (!wrap.contains(e.target)) hide(wrap);
    });
  });
})();

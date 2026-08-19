(() => {
  document.querySelectorAll("[data-combo]").forEach(combo => {
    const input = combo.querySelector(".combo-input");
    const list = combo.querySelector(".combo-list");
    const toggle = combo.querySelector(".combo-toggle");
    const options = Array.from(list.querySelectorAll(".combo-option"));
    let active = -1;

    const shown = () => options.filter(o => !o.hidden);

    const open = () => {
      list.hidden = false;
      combo.classList.add("is-open");
      input.setAttribute("aria-expanded", "true");
    };

    const close = () => {
      list.hidden = true;
      combo.classList.remove("is-open");
      input.setAttribute("aria-expanded", "false");
      highlight(-1);
    };

    // aria-activedescendant rather than moving focus: the caret has to stay in
    // the field while the arrow keys walk the list.
    const highlight = i => {
      active = i;
      options.forEach(o => {
        o.classList.remove("is-active");
        o.setAttribute("aria-selected", "false");
      });
      const el = shown()[i];
      if (!el) {
        input.removeAttribute("aria-activedescendant");
        return;
      }
      el.classList.add("is-active");
      el.setAttribute("aria-selected", "true");
      input.setAttribute("aria-activedescendant", el.id);
      el.scrollIntoView({ block: "nearest" });
    };

    // Matching the label as well as the value is what lets "github" find the
    // host and "everything" find the catch-all, which is spelled "*".
    const filter = () => {
      const q = input.value.trim().toLowerCase();
      options.forEach(o => {
        o.hidden = q !== "" && !o.textContent.toLowerCase().includes(q) &&
          !o.dataset.value.toLowerCase().includes(q);
      });
      highlight(-1);
      return shown().length;
    };

    const showAll = () => {
      options.forEach(o => { o.hidden = false; });
      highlight(-1);
    };

    const pick = el => {
      if (!el) return;
      input.value = el.dataset.value;
      close();
      input.focus();
    };

    input.addEventListener("input", () => { filter() ? open() : close(); });

    // Clicking an empty field is a request to see what is on offer; clicking
    // one that already holds a scope is almost always a request to edit it.
    input.addEventListener("click", () => {
      if (list.hidden && input.value.trim() === "") { showAll(); open(); }
    });

    // mousedown, not click: the default would pull focus out of the input and
    // close the list before the press lands.
    toggle.addEventListener("mousedown", e => {
      e.preventDefault();
      if (list.hidden) { showAll(); open(); input.focus(); } else { close(); }
    });

    list.addEventListener("mousedown", e => {
      const el = e.target.closest(".combo-option");
      if (!el) return;
      e.preventDefault();
      pick(el);
    });

    input.addEventListener("keydown", e => {
      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          if (list.hidden) { if (!filter()) showAll(); open(); }
          highlight(Math.min(active + 1, shown().length - 1));
          break;
        case "ArrowUp":
          if (list.hidden) return;
          e.preventDefault();
          highlight(Math.max(active - 1, 0));
          break;
        case "Enter":
          // Only swallowed while an option is picked out; otherwise Enter
          // submits the form, which is what a typed-in scope wants.
          if (!list.hidden && active >= 0) { e.preventDefault(); pick(shown()[active]); }
          else close();
          break;
        case "Escape":
          if (list.hidden) return;
          e.preventDefault();
          close();
          break;
      }
    });

    input.addEventListener("blur", () => { close(); });
  });
})();

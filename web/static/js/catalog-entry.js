(function () {
  const picks = document.querySelectorAll(".deploy-pick");
  const image = document.getElementById("image-fields");
  const compose = document.getElementById("compose-fields");
  for (const p of picks) {
    p.addEventListener("change", function () {
      image.classList.toggle("hidden", p.value !== "image");
      compose.classList.toggle("hidden", p.value !== "compose");
    });
  }

  const rows = document.getElementById("params");
  const blank = document.getElementById("param-row-template");
  document.getElementById("add-param").addEventListener("click", function () {
    rows.appendChild(blank.content.cloneNode(true));
    const added = rows.lastElementChild.querySelector("input");
    if (added) added.focus();
  });
  // Removing a row is delegated: the rows that arrive later have to answer
  // to it too, and they do not exist when this runs.
  rows.addEventListener("click", function (e) {
    const button = e.target.closest(".remove-param");
    if (button) button.closest(".param-row").remove();
  });
})();

(function () {
  // The example is the format's documentation, and the shortest path from
  // reading it to having one is not retyping it.
  const button = document.getElementById("use-example");
  const box = document.getElementById("new-yaml");
  const example = document.getElementById("example-doc");
  if (!button || !box || !example) return;
  button.addEventListener("click", function () {
    box.value = example.textContent;
    box.focus();
    box.setSelectionRange(0, 0);
  });
})();

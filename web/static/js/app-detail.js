(function () {
  var panel = document.getElementById('status-panel');
  var glow = document.getElementById('app-glow');
  if (!panel || !glow) return;

  // The panel is polled every few seconds, so the state is read back out of it
  // rather than held here: whatever it last swapped in is the truth.
  function stateNow() {
    var el = panel.querySelector('[data-app-state]');
    return el ? el.getAttribute('data-app-state') : '';
  }

  // Only the two states that are a verdict get a colour. "Deploying" and
  // "not deployed" are the middle of something, and lighting the page for
  // them would be announcing a result that does not exist yet.
  function tone(state) {
    if (state === 'running') return 'is-ok';
    if (state === 'stopped' || state === 'error') return 'is-failed';
    return '';
  }

  function play(state, poured) {
    var colour = tone(state);
    glow.classList.remove('is-ok', 'is-failed', 'is-brief', 'is-burst');
    if (!colour) return;
    void glow.offsetWidth; // let the removal take effect, so the run restarts
    glow.classList.add(colour, poured ? 'is-burst' : 'is-brief');
  }

  var last = stateNow();
  play(last, false);

  document.body.addEventListener('htmx:afterSwap', function (e) {
    if (e.target !== panel) return;
    var now = stateNow();
    if (now === last) return;
    // A deploy that has just finished is the one thing worth the full pour,
    // flash and all; a state that merely changed wells up quietly.
    play(now, last === 'deploying');
    last = now;
  });
})();

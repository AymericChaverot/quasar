(function () {
  var root = document.getElementById('updating');
  var boot = root.dataset.version;            // the version this page was served by
  var target = root.dataset.target;
  var phaseEl = document.getElementById('update-phase');
  var meter = document.getElementById('update-meter');
  var bar = meter.firstElementChild;
  var errEl = document.getElementById('update-error');
  var backEl = document.getElementById('update-back');
  var markEl = document.getElementById('update-mark');
  var glowEl = document.getElementById('update-glow');
  var brandEl = document.getElementById('update-brand');
  var steps = {};
  root.querySelectorAll('[data-step]').forEach(function (el) { steps[el.dataset.step] = el; });

  var deadline = Date.now() + 20 * 60 * 1000;
  var dropped = false;   // the dashboard has been unreachable at least once
  var finished = false;

  function stage(pull, swap, back) {
    var want = { pull: pull, swap: swap, back: back };
    Object.keys(want).forEach(function (name) {
      steps[name].classList.toggle('is-active', want[name] === 'active');
      steps[name].classList.toggle('is-done', want[name] === 'done');
    });
  }

  // A percentage the daemon has not reported yet is shown as movement rather
  // than as an empty bar, which would read as "nothing is happening".
  function progress(pct) {
    if (pct > 0) {
      meter.classList.remove('is-waiting');
      bar.style.width = Math.min(100, pct) + '%';
    } else {
      meter.classList.add('is-waiting');
      bar.style.width = '100%';
    }
  }

  // Everything that was moving because work was under way stops together with
  // the work. Leaving the heading swept under a cross would say the update is
  // still going, which is the one thing the outcome has just settled.
  function still() {
    brandEl.classList.remove('shimmer');
    phaseEl.classList.remove('shimmer');
  }

  function fail(msg) {
    finished = true;
    still();
    meter.hidden = true;
    markEl.classList.add('is-failed');
    glowEl.classList.add('is-failed', 'is-flare');
    // The step that was running is the step that broke: it takes a cross, and
    // nothing is left spinning under an error message as though work were
    // still going on.
    Object.keys(steps).forEach(function (name) {
      if (steps[name].classList.contains('is-active')) {
        steps[name].classList.remove('is-active');
        steps[name].classList.add('is-failed');
      }
    });
    phaseEl.textContent = 'The update stopped.';
    errEl.textContent = msg;
    errEl.hidden = false;
    backEl.hidden = false;
  }

  function done(v) {
    finished = true;
    still();
    stage('done', 'done', 'done');
    progress(100);
    markEl.classList.add('is-ok');
    glowEl.classList.add('is-ok', 'is-flare');
    phaseEl.textContent = 'Now running ' + v + '. Taking you back…';
    // Long enough for the tick to land and the light to fall back to its low
    // steady level; short enough that nobody is left on a finished screen.
    setTimeout(function () {
      location.replace('/system?msg=' + encodeURIComponent('Dashboard updated to ' + v + '.'));
    }, 2400);
  }

  function schedule(ms) {
    if (finished) return;
    if (Date.now() > deadline) {
      fail('The dashboard has not come back within twenty minutes. Check storage/update.log on the server, and whether the quasar-dashboard container is running.');
      return;
    }
    setTimeout(tick, ms);
  }

  function ask() {
    var ctrl = new AbortController();
    var timer = setTimeout(function () { ctrl.abort(); }, 5000);
    return fetch('/system/update/status', { cache: 'no-store', signal: ctrl.signal })
      .then(function (res) { clearTimeout(timer); return res; })
      .catch(function () { clearTimeout(timer); return null; });
  }

  function tick() {
    ask().then(function (res) {
      if (finished) return;

      // Followed a redirect: the session did not survive, so the answer is a
      // login page rather than a status.
      if (res && res.redirected) { location.replace(res.url); return; }

      if (!res || !res.ok) {
        // No answer at all: this container is being replaced right now, or the
        // proxy has nothing to route to yet. That is the expected middle of an
        // update, not a failure.
        dropped = true;
        stage('done', 'done', 'active');
        phaseEl.textContent = 'The dashboard is restarting. Waiting for it to answer again…';
        progress(0);
        schedule(1500);
        return;
      }

      res.json().then(function (s) {
        if (finished) return;

        // A different version answering can only mean the new container is up.
        if (s.version && s.version !== boot) { done(s.version); return; }

        if (s.phase === 'failed') {
          fail(s.error || 'The update did not complete.');
          return;
        }
        // Back from a restart, on the version we started from: the updater put
        // the old image back because the new one would not stay up.
        if (dropped && s.phase === '') {
          fail('The dashboard came back on ' + boot + ', so ' + target + ' did not take — it was rolled back to the version that works. storage/update.log on the server says why.');
          return;
        }

        if (s.phase === 'pulling') {
          stage('active', '', '');
          progress(s.percent);
          if (s.detail === 'Extracting') {
            phaseEl.textContent = 'Unpacking ' + target + '…';
          } else if (s.percent > 0) {
            phaseEl.textContent = 'Downloading ' + target + ' — ' + Math.round(s.percent) + '%';
          } else {
            phaseEl.textContent = 'Fetching ' + target + '…';
          }
        } else if (s.phase === 'handoff') {
          stage('done', 'active', '');
          progress(0);
          phaseEl.textContent = 'Restarting the dashboard on ' + target + '…';
        }
        schedule(1200);
      }, function () {
        // A 200 that is not the JSON we asked for: something else answered.
        dropped = true;
        schedule(1500);
      });
    });
  }

  stage('active', '', '');
  tick();
})();

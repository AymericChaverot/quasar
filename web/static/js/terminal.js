// The interactive shell, over a websocket, drawn by xterm.
//
// The application id comes from the mount point's data attribute rather than
// from the template: it is the one value this needs from the server, and a
// data attribute is how a file that is not a template gets one.
(function () {
  const mount = document.getElementById('terminal');
  if (!mount) return;

  const term = new Terminal({
    fontFamily: 'ui-monospace, "Cascadia Code", Menlo, monospace',
    fontSize: 13,
    theme: { background: getComputedStyle(document.documentElement).getPropertyValue('--log-bg').trim() || '#000' },
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(mount);
  fit.fit();

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/apps/${mount.dataset.appId}/terminal/ws`);
  ws.binaryType = 'arraybuffer';

  ws.onopen = () => {
    ws.send(JSON.stringify({ t: 'r', cols: term.cols, rows: term.rows }));
    term.focus();
  };
  ws.onmessage = (ev) => term.write(new Uint8Array(ev.data));
  ws.onclose = () => term.write('\r\n\x1b[2m[session closed]\x1b[0m\r\n');

  term.onData((data) => ws.send(JSON.stringify({ t: 'i', d: data })));
  term.onResize(({ cols, rows }) => ws.send(JSON.stringify({ t: 'r', cols, rows })));
  addEventListener('resize', () => fit.fit());
})();

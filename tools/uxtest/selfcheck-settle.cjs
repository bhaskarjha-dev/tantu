// Self-check for the boot-settled condition.
//
// The harness waited for `location.hash === ''` and called that "the page is
// done booting". That was a claim about the product's behaviour, written as a
// comment, and it was wrong -- see settle.cjs for the two lines that make it
// wrong. Wrong in the direction that produces a red build with a message
// blaming the Hub, which is worse than being red: it sends whoever reads it to
// the wrong subsystem.
//
// So this is a behaviour gate, not a unit test. It does not ask bootSettled()
// whether it agrees with itself; it serves a page that performs the product's
// exact sequence, drives a real browser, and observes which document the
// harness would be reading at the moment it decided to stop waiting.
//
// It asserts three things, each of which can fail:
//
//   1. The old condition really does settle on the pre-reload document. If this
//      ever stops holding, the product's ordering changed and the harness needs
//      re-examining -- the gate says so rather than passing on a stale premise.
//   2. The new condition never settles there; it waits for the post-bootstrap
//      document.
//   3. The pre-reload document renders no data, which is the mechanism: reading
//      the render stamp from it can only ever find nothing.
//
// Break 1: delete the reload() from the synthetic page. The gate fails at (1)
// and (2), because the race it is testing for no longer exists.
// Break 2: make bootSettled() ignore bootFragment. The gate fails at (2).
'use strict';

const http = require('http');
const { webkit } = require('playwright-core');
const { bootSettled, describeBootState, BOOT_KEY } = require('./settle.cjs');

const failures = [];

// The two product lines, verbatim and in order (dashboard.html:1686-1689), plus
// the status slot the render stamp is taken from. Nothing else, so what is
// observed here is the ordering and not a coincidence of a fuller page.
const PAGE = `<!doctype html><meta charset="utf-8"><title>settle</title>
<span id="idTransport">-</span>
<script>
  history.replaceState(null, '', location.pathname + location.search);
  window.location.reload();
</script>
<script>
  // Only the document that survives the reload has a session cookie, so only it
  // is given data. The pre-reload document keeps the "-" placeholder.
  if (!window.__tantuBootFragment) {
    document.getElementById('idTransport').textContent = 'loopback';
  }
</script>`;

// The init script run.mjs will carry. Kept here in the same shape so the gate
// tests the arrangement that ships, not an idea of it.
const INIT = () => {
  const m = /tantu_bootstrap=([^&]*)/.exec(location.hash || '');
  window.__tantuBootFragment = m ? m[1] : null;
  window.__docId = Math.random().toString(36).slice(2, 10);
};

const READ = `JSON.stringify({
  hash: location.hash,
  bootFragment: window.__tantuBootFragment === undefined ? 'UNREADABLE' : window.__tantuBootFragment,
  docId: window.__docId === undefined ? 'UNREADABLE' : window.__docId,
  transport: (document.getElementById('idTransport') || {}).textContent
})`;

// Poll for a condition, recording the first state that satisfied it.
//
// A null read is not "no" and not an error to abandon on: it is an evaluate
// that landed while the reload was tearing the document down, which is the very
// window under test. It is retried, and counted, because the count is how wide
// the window was on this machine -- zero on a fast reload, many on a loaded
// runner, which is the whole reason this reproduced on CI and never locally.
async function waitFor(page, predicate) {
  let last = null;
  let torn = 0;
  let reads = 0;
  for (let i = 0; i < 40; i++) {
    const raw = await page.evaluate(READ).catch(() => null);
    if (raw === null) { torn++; continue; }
    reads++;
    const state = JSON.parse(raw);
    last = state;
    if (predicate(state)) return { settled: true, state, torn, reads };
    await new Promise(r => setTimeout(r, 25));
  }
  return { settled: false, state: last, torn, reads };
}

(async () => {
  const server = http.createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end(PAGE);
  });
  await new Promise(r => server.listen(0, '127.0.0.1', r));
  const port = server.address().port;

  const browser = await webkit.launch({ headless: true });
  try {
    const page = await browser.newPage();
    await page.addInitScript(INIT);
    await page.goto(`http://127.0.0.1:${port}/#${BOOT_KEY}=abc123`);

    // (1) The condition the harness used: the fragment is gone. This is the
    // claim under test, so it is observed rather than assumed.
    const old = await waitFor(page, (s) => s.hash === '');
    if (!old.settled) {
      failures.push('the fragment never became empty, so this gate cannot observe ' +
        'the window it exists to test -- the product page no longer strips it');
    } else {
      const onPreReload = old.state.bootFragment !== null;
      if (!onPreReload) {
        failures.push('"the fragment is gone" settled on the post-bootstrap document, '
          + 'so the pre-reload window this gate was written for no longer occurs; the '
          + 'settle condition may be simplifiable and dashboard.html:1686-1689 must be '
          + 're-read before the rationale in settle.cjs is trusted again');
      }
    }

    // (3) The mechanism. Read again on whichever document is live: if the
    // harness read the stamp from the pre-reload document it would find
    // nothing, because that document has no session and correctly renders "-".
    if (old.settled && old.state.bootFragment !== null && old.state.transport !== '-') {
      failures.push('the pre-reload document rendered data ("' + old.state.transport +
        '"); the premise that it cannot is what makes the wrong-settle condition '
        + 'harmless-but-unnoticed, and it no longer holds');
    }

    // (2) The replacement condition, on a fresh load.
    const page2 = await browser.newPage();
    await page2.addInitScript(INIT);
    await page2.goto(`http://127.0.0.1:${port}/#${BOOT_KEY}=abc123`);
    const fresh = await waitFor(page2, (s) => bootSettled(s));
    if (!fresh.settled) {
      failures.push('bootSettled() never became true: ' + describeBootState(fresh.state) +
        ' -- the reload is not landing, so the harness would now time out where it ' +
        'previously reported a wrong document (' + fresh.reads + ' readable polls, ' +
        fresh.torn + ' torn down mid-reload)');
    } else if (fresh.state.bootFragment !== null) {
      failures.push('bootSettled() returned true on the pre-reload document (saw ' +
        'tantu_bootstrap=' + fresh.state.bootFragment + '), which is the exact defect ' +
        'it exists to remove');
    } else if (fresh.state.transport === '-') {
      failures.push('bootSettled() returned true on a document with no data (still "-"); ' +
        'the settled document must be the one that has a session');
    }
    await page2.close();
    await page.close();
  } finally {
    await browser.close();
    server.close();
  }

  if (failures.length) {
    console.error('selfcheck-settle: ' + failures.length + ' failure(s)');
    failures.forEach(f => console.error('  FAIL ' + f));
    process.exit(1);
  }
  console.log('selfcheck-settle: the old condition settles pre-reload, the new one waits for the post-bootstrap document');
})().catch(e => { console.error('selfcheck-settle FAILED: ' + e.message); process.exit(1); });
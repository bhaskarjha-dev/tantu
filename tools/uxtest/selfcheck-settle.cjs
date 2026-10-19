// Self-check for the boot-settled condition.
//
// The harness used to wait for `location.hash === ''` and called that "the page
// is done booting". That was a claim about the product's behaviour, written as a
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
// It asserts four things, each of which can fail:
//
//   1. The product produces a document on which the fragment is already gone and
//      the boot is not finished. That is the mechanism: the fragment identifies
//      the document, "the hash is empty" does not.
//   2. "The fragment is gone" is true on that document *and* on the one that
//      follows it, so it cannot tell the two apart. That is the defect, stated
//      as a property instead of as a race.
//   3. bootSettled() rejects the first document and accepts the second.
//   4. The pre-reload document renders no data, so a harness that took its
//      render stamp from it finds nothing -- the consequence behind the
//      original misdiagnosis.
//
// The reload is withheld until the pre-reload state has been read, on purpose.
// The previous version of this gate fired the reload immediately and then
// asserted that the old condition settled on the pre-reload document. That is a
// race between a poll and a reload, so it asserted almost nothing about the
// product: it passed on a developer machine and failed on a CI runner on
// 2026-10-19 with no change to dashboard.html at all, in a message blaming the
// dashboard for a harness timing accident. Holding the reload makes the
// intermediate state something the gate produces rather than something it hopes
// to catch before the browser gets there first. The product's ordering -- strip,
// then reload -- is unchanged; only the reload waits for the observer.
//
// Break 1: delete the replaceState() from the page. The gate fails at (1).
// Break 2: make bootSettled() ignore bootFragment. The gate fails at (3).
// Break 3: give the pre-reload document data. The gate fails at (4).
// Break 4: delete the reload. The gate fails at (3), on the second document.
'use strict';

const http = require('http');
const { webkit } = require('playwright-core');
const { bootSettled, describeBootState, BOOT_KEY } = require('./settle.cjs');

const failures = [];

// The two product steps, verbatim and in order from bootstrapDashboard()
// (dashboard.html:1746 strips, :1749 reloads), plus the status slot the render
// stamp is taken from. Nothing else, so what is observed here is the ordering
// and not a coincidence of a fuller page.
//
// Step two is exposed as __releaseReload instead of being called inline. The
// signal is set either way, so a missing strip reports as a missing strip rather
// than as a timeout.
const PAGE = `<!doctype html><meta charset="utf-8"><title>settle</title>
<span id="idTransport">-</span>
<script>
  history.replaceState(null, '', location.pathname + location.search);
  window.__preReloadReady = true;
  window.__releaseReload = function () { window.location.reload(); };
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
  preReloadReady: window.__preReloadReady === true,
  transport: (document.getElementById('idTransport') || {}).textContent
})`;

// Poll for a condition, recording the first state that satisfied it.
//
// A null read is not "no" and not an error to abandon on: it is an evaluate
// that landed while the reload was tearing the document down. It is retried, and
// counted, because the count is how wide the teardown was on this machine.
async function waitFor(page, predicate, attempts) {
  let last = null;
  let torn = 0;
  let reads = 0;
  for (let i = 0; i < attempts; i++) {
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

    // (1) The mechanism. Stop on the document the product leaves behind after
    // stripping: fragment gone, reload not yet released, so this is the live
    // document and it is the pre-bootstrap one. Nothing here is a race -- the
    // page cannot move on until this read has happened.
    const pre = await waitFor(page, (s) => s.preReloadReady && s.hash === '', 60);
    if (!pre.settled) {
      const s = pre.state || {};
      if (s.preReloadReady && s.hash !== '') {
        failures.push('the page reported the strip was done but location.hash is still '
          + JSON.stringify(s.hash) + ', so the product sequence the harness has to '
          + 'tolerate no longer occurs and this gate cannot observe it');
      } else {
        failures.push('never reached the stripped, pre-reload document (' + pre.reads
          + ' readable polls, ' + pre.torn + ' unreadable): ' + describeBootState(s)
          + ' -- the page did not run the product sequence, so nothing here can say '
          + 'whether the settle condition is right');
      }
    } else if (pre.state.bootFragment !== 'abc123') {
      failures.push('the stripped document did not report the token it saw (saw '
        + JSON.stringify(pre.state.bootFragment) + '), so it cannot be established '
        + 'that this is the pre-bootstrap document and the rest of this gate has '
        + 'nothing to stand on');
    }

    if (pre.settled) {
      // (4) The mechanism behind the original misdiagnosis: this document has no
      // session, so it correctly renders no data and a render stamp taken from it
      // reads as absent.
      if (pre.state.transport !== '-') {
        failures.push('the pre-reload document rendered data ("' + pre.state.transport
          + '"); the premise that it cannot is what made the old condition '
          + 'harmless-but-unnoticed, and it no longer holds');
      }

      // (3a) The replacement must reject the document the old condition accepted.
      if (bootSettled(pre.state)) {
        failures.push('bootSettled() returned true on the pre-reload document (fragment '
          + 'gone, token ' + pre.state.bootFragment + '), which is the exact defect it '
          + 'exists to remove');
      }
    }

    // Release the reload and follow the same page to its successor, which is the
    // document the harness is supposed to end up reading.
    await page.evaluate('window.__releaseReload()').catch(() => null);
    const post = await waitFor(page, (s) => bootSettled(s), 80);

    // (3b) ...and accept the one that follows it.
    if (!post.settled) {
      failures.push('bootSettled() never became true after the reload was released: '
        + describeBootState(post.state) + ' -- the reload is not landing, so the '
        + 'harness would now time out where it previously reported a wrong document ('
        + post.reads + ' readable polls, ' + post.torn + ' torn down mid-reload)');
    } else if (post.state.transport === '-') {
      failures.push('bootSettled() returned true on a document with no data (still "-"); '
        + 'the settled document must be the one that has a session');
    }

    // (2) The defect, as a property. The old condition holds on both documents, so
    // it names neither. This is the assertion that did not need a race to make.
    if (pre.settled && post.settled && pre.state.docId === post.state.docId) {
      failures.push('the pre-reload and post-reload reads came from the same document '
        + '(' + post.state.docId + '), so the reload never happened and the two states '
        + 'being compared are not two documents at all');
    }
    if (pre.settled && post.settled) {
      console.log('selfcheck-settle: the old condition is true on both documents (pre '
        + pre.state.docId + ' hash ' + JSON.stringify(pre.state.hash) + ', post '
        + post.state.docId + ' hash ' + JSON.stringify(post.state.hash)
        + ') -- it cannot tell them apart, which is the defect');
    }
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
  console.log('selfcheck-settle: the old condition is true on both documents, bootSettled() only on the second');
})().catch(e => { console.error('selfcheck-settle FAILED: ' + e.message); process.exit(1); });
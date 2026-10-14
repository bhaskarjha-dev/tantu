// Self-check for the harness's own OVERFLOW_PROBE.
//
// run.mjs uses that probe to name the elements behind a narrow-viewport
// overflow. A diagnostic that has only ever run against a clean page is a
// diagnostic nobody has tested, and the failure it exists to explain -- 21px of
// overflow in Firefox and WebKit on the CI runners, reproducible across two
// engines and not on any machine here -- depends entirely on it.
//
// It also caught a blind spot that would have made it useless. The first version
// measured only border boxes, and reported a page whose document was 924 wide as
// entirely clean: an unbreakable string does not widen the block containing it,
// so the culprit's own box was 360 wide and it never appeared. The probe now
// measures text as well, and this file is what proves it does.
//
// Runs against a synthetic page rather than the product, so it needs no Hub and
// no build. Chromium is used only because it is the cheapest engine to launch;
// the probe itself is plain DOM and runs identically on all three.

const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');

const REPO = path.resolve(__dirname, '..', '..');

function extractProbe() {
  const src = fs.readFileSync(path.join(REPO, 'tools', 'uxtest', 'run.mjs'), 'utf8');
  const m = src.match(/const OVERFLOW_PROBE = `([\s\S]*?)`;/);
  if (!m) {
    console.error('selfcheck: could not find OVERFLOW_PROBE in run.mjs. If the probe was ' +
      'renamed or reformatted, this self-check needs updating -- and until it does, a broken ' +
      'overflow diagnostic would fail silently on the platform that needs it.');
    process.exit(1);
  }
  return m[1];
}

(async () => {
  const probe = extractProbe();
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 360, height: 780 } });
  const failures = [];

  // A page that fits must report no culprits. Without this the probe could name
  // something on every clean page and still be useless.
  await page.setContent('<html><body><div style="width:300px">fine</div></body></html>');
  const clean = await page.evaluate(probe);
  if (clean.w > clean.c) failures.push(`a fitting page reported w=${clean.w} > c=${clean.c}`);
  if (clean.culprits.length !== 0) {
    failures.push(`a fitting page named culprits: ${JSON.stringify(clean.culprits)}`);
  }

  // Two distinct causes, because they need different instruments:
  //   - an oversized element, which any box measurement catches;
  //   - an unbreakable string, whose box is the container's width and which only
  //     a text measurement catches.
  await page.setContent(
    '<html><body style="margin:0">' +
    '<div style="width:500px">too wide</div>' +
    '<div id="longtext" style="white-space:nowrap">' + 'A'.repeat(80) + '</div>' +
    '</body></html>'
  );
  const dirty = await page.evaluate(probe);
  if (dirty.w <= dirty.c) {
    failures.push('an overflowing page was not detected');
  }
  const namesBox = dirty.culprits.some((c) => c.why === 'box');
  const namesText = dirty.culprits.some((c) => c.sel.includes('#longtext') && c.why === 'text');
  if (!namesBox) failures.push('did not name the oversized element as a box overflow');
  if (!namesText) failures.push('did not name the unbreakable string as a text overflow');

  // Ranking. An element inside a horizontally scrolling container is wider than
  // the viewport by design, and it must rank *below* a real page-level overflow.
  // Getting this wrong is what buried the original defect: three off-screen tab
  // buttons in a scroll strip reported up to 501px and took every slot, while
  // the send button that actually caused the 21px scroll sat fourth.
  await page.setContent(
    '<html><body style="margin:0">' +
    '<div style="overflow-x:auto"><button id="instrip" style="width:900px">wide, on purpose</button></div>' +
    '<div id="realcause" style="white-space:nowrap">' + 'B'.repeat(80) + '</div>' +
    '</body></html>'
  );
  const ranked = await page.evaluate(probe);
  if (ranked.w <= ranked.c) failures.push('the ranking page was not detected as overflowing');
  const strip = ranked.culprits.find((c) => c.sel.includes('#instrip'));
  const cause = ranked.culprits.find((c) => c.sel.includes('#realcause'));
  if (strip && strip.inScroller !== true) {
    failures.push('an element inside an overflow-x container was not marked inScroller');
  }
  if (!cause || cause.inScroller !== false) {
    failures.push('the genuine page-level overflow was not marked as such');
  }
  if (cause && strip && ranked.culprits.indexOf(cause) > ranked.culprits.indexOf(strip)) {
    failures.push('a scroll-contained element ranked above the real page-level overflow');
  }

  await browser.close();

  if (failures.length) {
    for (const f of failures) console.error('selfcheck: ' + f);
    process.exit(1);
  }
  console.log('uxtest selfcheck: the overflow probe detects box and text overflow, ' +
    'reports nothing on a page that fits, and ranks a real page-level overflow ' +
    'above a scroll-contained element');
})().catch((e) => { console.error('selfcheck: ' + (e && e.message)); process.exit(1); });
// Self-check: a card header that could not fit its own title.
//
// The defect was a card header row whose title and buttons together were
// wider than the card. On the Linux runner's font, "Recent
// Authorizations" plus "Refresh" and "Clear" measured 362px in a 360px
// viewport: the actions were pushed two pixels past the edge of the
// screen, and the page gained a horizontal scrollbar that existed on
// that runner and on no machine with a narrower font. It survived
// several batches of narrow-viewport work for exactly that reason.
//
// Testing the viewport alone cannot reproduce it here. This forces the
// condition instead, by giving the card headers a deliberately wide font
// stack.
//
// What is measured is the CARD HEADER'S OWN CONTENT, not the document's
// width, and the difference matters. The document's width is the sum of
// everything on the page, so a page with a session banner, a stale
// banner or a long log line is a different measurement from a page
// without one, and a gate that measures the document is measuring the
// page rather than the thing it is about. The card header has to fit
// its own contents, whatever else the page is showing.
//
// The content is measured against the header's content box, and the
// children are measured rather than the header's scrollWidth: a flex row
// that cannot fit its items does not widen the element containing it, so
// a box-only measurement would report the page as clean. That is the
// same trap the harness's overflow probe had to be taught about.
//
// Every tab is visited, because the card headers live on four different
// tabs and only one of them was reported.
//
// It is run in CI rather than only locally so the narrow-viewport work
// has a gate that does not depend on the runner's font stack matching a
// developer's.

const { chromium } = require('playwright');
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const REPO = path.resolve(__dirname, '..', '..');
// A wider stack than the default proportional UI font, standing in for the
// fallback faces the CI runners resolve.
const WIDE_FONT = '"Courier New", "DejaVu Sans Mono", monospace';
const TABS = ['tab-drop', 'tab-relay', 'tab-peers', 'tab-transfers', 'tab-logs'];

// For every card header on the page: how far its contents run past its own
// content box.
const MEASURE = `(function () {
  var out = [];
  var heads = document.querySelectorAll('.card-head');
  for (var i = 0; i < heads.length; i++) {
    var el = heads[i];
    var box = el.getBoundingClientRect();
    if (!box.width) continue;
    var cs = getComputedStyle(el);
    var inner = box.right - (parseFloat(cs.paddingRight) || 0);
    var worst = 0, who = '';
    for (var j = 0; j < el.children.length; j++) {
      var c = el.children[j];
      var cr = c.getBoundingClientRect();
      if (cr.right - inner > worst) {
        worst = cr.right - inner;
        who = c.tagName.toLowerCase() + (c.id ? '#' + c.id : '') +
          (typeof c.className === 'string' && c.className.trim()
            ? '.' + c.className.trim().split(/\\s+/).slice(0, 2).join('.') : '') +
          ' "' + (c.textContent || '').trim().replace(/\\s+/g, ' ').slice(0, 32) + '"';
      }
    }
    if (worst > 0.5) {
      out.push({ over: Math.round(worst * 10) / 10, who: who, right: Math.round(box.right) });
    }
  }
  var d = document.documentElement;
  return { heads: out, scrollW: d.scrollWidth, clientW: d.clientWidth };
})()`;

function hubPort(proc) {
  return new Promise((res, rej) => {
    let out = '';
    const t = setTimeout(() => rej(new Error('the Hub never reported a web port:\n' + out)), 30000);
    proc.stdout.on('data', (d) => { out += d; });
    proc.stderr.on('data', (d) => { out += d; });
    const iv = setInterval(() => {
      const m = out.match(/Web Dashboard:\s*http:\/\/127\.0\.0\.1:(\d+)/);
      if (m) { clearInterval(iv); clearTimeout(t); res(m[1]); }
    }, 150);
  });
}

(async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'tantu-cardhead-'));
  const exe = path.join(tmp, 'tantu.exe');
  await new Promise((res, rej) => {
    const p = spawn('go', ['build', '-o', exe, './cmd/tantu'], { cwd: REPO, stdio: 'inherit' });
    p.on('exit', (c) => (c === 0 ? res() : rej(new Error('build failed'))));
  });
  const store = path.join(tmp, 'store');
  fs.mkdirSync(store, { recursive: true });
  const hub = spawn(exe, ['hub', '--headless', '--transport', 'loopback',
    '--web-addr', '127.0.0.1:0', '--store-dir', store], { stdio: ['ignore', 'pipe', 'pipe'] });
  const port = await hubPort(hub);

  const browser = await chromium.launch();
  const failures = [];
  const rows = [];
  for (const wide of [false, true]) {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    await page.goto(`http://127.0.0.1:${port}/`, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1200);
    if (wide) {
      await page.addStyleTag({ content: `.card-head,.card-head *{font-family:${WIDE_FONT};}` });
      await page.waitForTimeout(250);
    }
    for (const width of [360, 320]) {
      await page.setViewportSize({ width, height: 780 });
      await page.waitForTimeout(250);
      for (const tab of TABS) {
        await page.evaluate((t) => {
          const b = document.querySelector('[data-tab="' + t + '"]');
          if (b) b.click();
        }, tab);
        await page.waitForTimeout(250);
        const m = await page.evaluate(MEASURE);
        rows.push({ font: wide ? 'wide' : 'native', width, tab, ...m });
        for (const h of m.heads) {
          failures.push(`${wide ? 'wide' : 'native'} font at ${width}px on ${tab}: a card header's ` +
            `contents run ${h.over}px past its own box (${h.who})`);
        }
      }
    }
    await page.close();
  }
  await browser.close();
  try { hub.kill(); } catch (_) {}
  try { fs.rmSync(tmp, { recursive: true, force: true }); } catch (_) {}

  console.log('  selfcheck: ' + rows.length + ' combinations measured (native and wide font, ' +
    '320px and 360px, five tabs)');
  for (const f of failures) console.error('selfcheck: ' + f);
  if (failures.length) {
    console.error('selfcheck: a card header cannot fit its own title and its actions. When ' +
      'they do not fit on one line the actions have to go on their own line, and that has ' +
      'to be true of every card header rather than of the four somebody remembered: ' +
      '`.card-head` in dashboard.html carries `flex-wrap: wrap` for exactly that, and it ' +
      'is the rule every card header uses.');
    process.exit(1);
  }
  console.log('uxtest selfcheck: every card header fits its own title and its actions at 320px ' +
    'and 360px, on any tab, with the native font or a deliberately wide one');
})().catch((e) => { console.error('selfcheck: ' + (e && e.message)); process.exit(1); });

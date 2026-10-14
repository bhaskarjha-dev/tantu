// Self-check: the narrow-viewport button fix, verified under the condition that
// actually broke it.
//
// The defect was a send button whose label ("Send text to this Hub (local-only
// test)") could not wrap, because `.btn-primary` carries `white-space: nowrap`
// for good reason at desktop widths. At 360px that label ran 21-22px past its own
// box in Firefox and WebKit on CI and not at all in Chromium, because their
// default button font is wider than this machine's. The page gained a horizontal
// scrollbar that existed on two of three engines and on no machine but the
// runner's -- which is why it survived several batches of narrow-viewport work.
//
// Testing the viewport alone cannot reproduce it here. This forces the condition
// instead, by giving the buttons a deliberately wide font stack, and measures the
// label's text range against its own box -- because an unbreakable label does not
// widen the block containing it, so a box-only measurement would report the page
// as clean. That is the same trap the harness's overflow probe had to be taught
// about, and this file is where both are held to it.
//
// It is run in CI rather than only locally so the narrow-viewport work has a gate
// that does not depend on the runner's font stack matching a developer's.

const { chromium } = require('playwright');
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const REPO = path.resolve(__dirname, '..', '..');
// A wider stack than the default proportional UI font, standing in for the
// fallback faces the CI runners resolve.
const WIDE_FONT = '"Courier New", "DejaVu Sans Mono", monospace';
const LABEL = 'Send text to this Hub (local-only test)';

const MEASURE = `(function () {
  var b = document.getElementById('btnSendText');
  b.textContent = ${JSON.stringify(LABEL)};
  var r = b.getBoundingClientRect();
  var range = document.createRange();
  range.selectNodeContents(b);
  var t = range.getBoundingClientRect();
  var vw = document.documentElement.clientWidth;
  return {
    boxW: Math.round(r.width),
    textW: Math.round(t.width),
    docScrollW: document.documentElement.scrollWidth,
    clientW: vw,
    overflowing: document.documentElement.scrollWidth > vw,
  };
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
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'tantu-btnfix-'));
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
    await page.goto(`http://127.0.0.1:${port}/`, { waitUntil: 'networkidle' });
    await page.waitForTimeout(700);
    for (const width of [360, 320]) {
      await page.setViewportSize({ width, height: 780 });
      await page.waitForTimeout(250);
      if (wide) {
        await page.addStyleTag({ content: `.btn-primary,.btn,.btn-secondary,.btn-ghost{font-family:${WIDE_FONT};}` });
        await page.waitForTimeout(250);
      }
      const m = await page.evaluate(MEASURE);
      rows.push({ font: wide ? 'wide' : 'native', width, ...m });
      if (m.overflowing) {
        failures.push(`${wide ? 'wide' : 'native'} font at ${width}px: document is ` +
          `${m.docScrollW} wide in a ${m.clientW} viewport (label text ${m.textW} in a ` +
          `${m.boxW} button)`);
      }
    }
    await page.close();
  }
  await browser.close();
  try { hub.kill(); } catch (_) {}
  try { fs.rmSync(tmp, { recursive: true, force: true }); } catch (_) {}

  for (const r of rows) {
    console.log(`  selfcheck: ${r.font} font @ ${r.width}px -> document ` +
      `${r.docScrollW}/${r.clientW}, label ${r.textW} in ${r.boxW} button` +
      (r.overflowing ? '  OVERFLOW' : ''));
  }
  if (failures.length) {
    for (const f of failures) console.error('selfcheck: ' + f);
    console.error('selfcheck: a button label overflows the narrow viewport. Either ' +
      '`white-space: nowrap` has come back onto the narrow-viewport rule in ' +
      'dashboard.html, or the label has outgrown it.');
    process.exit(1);
  }
  console.log('uxtest selfcheck: no button label overflows at 320px or 360px, with the ' +
    'native font or a deliberately wide one');
})().catch((e) => { console.error('selfcheck: ' + (e && e.message)); process.exit(1); });
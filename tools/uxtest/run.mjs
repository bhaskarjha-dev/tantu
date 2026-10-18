// Dashboard acceptance harness: a real browser driven through Playwright,
// across three engines (chromium, firefox, webkit).
//
// It builds and starts an isolated Hub, seeds a peer store, drives the real
// dashboard, and asserts on measured runtime behaviour: computed contrast in
// both OS themes, real keyboard focus order and visibility, 200% zoom, a narrow
// viewport, reduced motion, and the console error log.
//
// A marker test cannot catch a blocked image, a destroyed focus ring, or a
// live region that announces twice a second. That is why this exists.
//
// Usage: node tools/uxtest/run.mjs [--peers] [--browser=chromium|firefox|webkit]
//   --peers    seed three trusted peers, exercising the multi-peer layout
//   --browser  engine to drive (default: chromium, or TANTU_TEST_BROWSER)
//
// TANTU_TEST_SHOTDIR=<dir> writes the screenshots outside the work
// directory, so they survive the run instead of being deleted with it.
// The work directory holds the generated identity and its private key, so
// it should keep being deleted; the screenshots should not have been in
// it. CI sets this and uploads the result, which is the only reason anyone
// has ever seen what this harness renders.
//
// Requires: Node 18+, `npm ci` in this directory, and the browser builds from
// `npx playwright install chromium firefox webkit`. None of that is needed to
// build, test, or run Tantu itself.

import { spawn, execFileSync } from 'node:child_process';
import { createEngineFailureNote } from './engine-failure.cjs';
import { bootSettled, describeBootState } from './settle.cjs';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, firefox, webkit } from 'playwright';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');
const WORK = path.join(os.tmpdir(), 'tantu-uxtest-' + process.pid);
const WEB_PORT = 18976;
const P2P_PORT = 19877;
// The two-Hub scenario needs a second web and P2P pair, well clear of both
// the harness's own ports and anything a developer's Hub might hold.
const WEB_PORT_A2 = 18990, P2P_PORT_A2 = 19890;
const WEB_PORT_B = 18991, P2P_PORT_B = 19891;
const WITH_PEERS = process.argv.includes('--peers');
const TWO_HUB = process.argv.includes('--two-hub');
const ENGINES = { chromium, firefox, webkit };
const ENGINE = (() => {
  const arg = process.argv.find((a) => a.startsWith('--browser='));
  const v = (arg ? arg.split('=')[1] : process.env.TANTU_TEST_BROWSER) || 'chromium';
  return String(v).toLowerCase();
})();

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const checks = [];
// Set when the Hub under test exits. Every check afterwards would otherwise fail
// for one reason -- there is no server -- and each would report it in its own
// vocabulary, which is how a killed Hub previously presented as four unrelated
// product defects. Recorded once, here, so the run can say so instead.
let hubDeath = null;
let lastHubOut = '';
// Set when the *engine* fails a request to the Hub, rather than the Hub
// answering wrongly. Same purpose as hubDeath and the same reasoning: one cause,
// recorded once, so the run says so instead of presenting every consequential
// failure as an independent finding.
//
// This exists because on 2026-10-16 a webkit run on the runner reported six
// failures -- an unmeasured render, a rejected preview file, a reconnect
// reporting -1ms, a staleness banner that never cleared, a notification that did
// not persist, and unexpected console errors -- when the console errors
// themselves said `WebKit encountered an internal error` against two Hub
// endpoints. Five of the six were downstream of the sixth, and nothing in the
// output said so. Reading that log costs an hour and proves nothing, because the
// one line that explains all of it is the one buried in the middle.
//
// Only the phrasing actually observed is matched. Adding other engines'
// phrasings is a change to make when one is seen failing, not to guess at in
// advance: a pattern loose enough to catch a legitimate resource failure would
// mislabel a product defect as an engine defect, which is worse than saying
// nothing at all.
const noteEngineFailure = createEngineFailureNote();
let engineFailure = null;
function check(name, ok, detail) {
  // The engine note goes first when both are true, because it is the more
  // specific cause: a Hub that exited during an engine failure is still a Hub
  // that exited, and saying only "the engine failed a request" would point
  // away from the real cause.
  engineFailure = noteEngineFailure.last;
  const suffix = hubDeath
    ? ' [HUB DIED: ' + hubDeath + ']'
    : (engineFailure ? ' [ENGINE FAILED THE REQUEST: ' + engineFailure + ']' : '');
  checks.push({ name, ok: !!ok, detail: String(detail ?? '').slice(0, 240) + suffix });
  if (!ok) console.log('  FAIL  ' + name + '  ' + String(detail ?? '').slice(0, 200) + suffix);
}
// A skip is never a pass. It gets its own line, its own count in the summary,
// and a printed reason -- because a check that quietly stops running is how a
// gate ends up covering nothing while still reporting green, which this
// repository has now hit several times. The clipboard behaviour checks need a
// real ClipboardEvent carrying a real DataTransfer, and not every engine will
// produce one, so that capability is measured rather than assumed.
const skips = [];
function skip(name, reason) {
  skips.push({ name, reason: String(reason).slice(0, 240) });
  console.log('  SKIP  ' + name + '  -- ' + String(reason).slice(0, 200));
}
// hubDied is where a red run is classified as an infrastructure failure rather
// than a product result. It is a hard stop: continuing would bury the cause
// under dozens of consequential failures, which is what made the previous
// occurrence expensive to diagnose.
function hubDied(context) {
  throw new Error('the Hub under test exited during ' + context + ' (' + hubDeath + ').\n'
    + 'Everything after that point fails for one reason only, so this is an\n'
    + 'environment problem rather than a product result. Last Hub output:\n'
    + (lastHubOut.trim() || '(none)'));
}
function go(args, opts = {}) {
  return execFileSync('go', args, { cwd: REPO, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], ...opts });
}
function git(args, opts = {}) {
  return execFileSync('git', args, { cwd: REPO, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], ...opts });
}

// ---------------------------------------------------------------- seed peers
function seedPeers(storeDir) {
  const peers = {
    a1: { fingerprint: 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90', name: 'devbox', alias: 'Devbox (workstation)', is_default: true, address: '192.168.1.50:9877', cert_pem: null, first_seen: '2026-09-01T09:00:00Z', last_seen: '2026-09-26T23:40:00Z' },
    b1: { fingerprint: 'b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1', name: 'build-box-with-a-deliberately-long-hostname-that-should-truncate-cleanly', address: '10.0.12.7:9877', cert_pem: null, first_seen: '2026-08-14T11:20:00Z', last_seen: '2026-09-20T08:05:00Z' },
    c1: { fingerprint: 'c3', name: 'tiny-fp', address: 'not-a-host:0', cert_pem: null, first_seen: '2026-07-01T00:00:00Z', last_seen: '2026-07-01T00:00:00Z' },
    // A blackhole peer, and the reason the cancel budget can be measured at
        // all without a second machine.
        //
    // 192.0.2.0/24 is TEST-NET-1 (RFC 5737): reserved for documentation and
    // unroutable by design, so a dial to it hangs instead of being refused in
    // milliseconds. That is the whole trick. A refused dial leaves no window
    // in which a user could cancel anything, so the cancel-acknowledgement
    // budget would be untestable against it. Same idea as the deterministic
    // leak gate in relay_cancel_test.go.
    //
    // Deliberately not the default peer: making it default would leave every
    // other check in this run waiting on a timeout.
    d1: { fingerprint: 'd4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3', name: 'unreachable-peer', alias: 'Unreachable (blackhole)', address: '192.0.2.1:9877', cert_pem: null, first_seen: '2026-10-07T00:00:00Z', last_seen: '2026-10-07T00:00:00Z' },
  };
  const out = {};
  for (const [k, p] of Object.entries(peers)) out[p.fingerprint] = p;
  fs.writeFileSync(path.join(storeDir, 'peers.json'), JSON.stringify(out, null, 2));
}

// ------------------------------------------------------------------ the suite
async function main() {
  const launcher = ENGINES[ENGINE];
  if (!launcher) {
    console.error('Unknown engine "' + ENGINE + '". Use --browser=chromium|firefox|webkit (or TANTU_TEST_BROWSER).');
    process.exitCode = 2; return;
  }
  const store = path.join(WORK, 'store');
  // Outside WORK when asked, so the cleanup below cannot take the evidence
  // with it. Labelled per topology and engine because CI runs six of these
  // in one job and they all write here.
  const SHOT_ROOT = process.env.TANTU_TEST_SHOTDIR;
  // Two-Hub is a suffix of its own: it runs on the same engine and topology as
  // an ordinary run, and without this its screenshots would overwrite that
  // run's instead of sitting beside them.
  const RUN_LABEL = ENGINE + (WITH_PEERS ? '-peers' : '') + (TWO_HUB ? '-twohub' : '');
  const out = SHOT_ROOT ? path.join(SHOT_ROOT, RUN_LABEL) : path.join(WORK, 'out');
  if (SHOT_ROOT) fs.mkdirSync(out, { recursive: true });
  // The binary is deliberately NOT named "tantu". This harness spawns a real
  // Hub, and a developer with their own Hub running will restart it with
  // something like `Stop-Process -Name tantu` -- which, when this exe carried
  // the same name, silently killed the harness's Hub mid-run. The failure did
  // not look like what it was: four scattered dashboard checks failed with a
  // staleness sentinel, a still-visible banner and "could not connect to
  // server", i.e. it read as product flakiness. It was misdiagnosed twice
  // before the cause was found. A distinct process identity means that command
  // cannot reach this process at all.
  const exe = path.join(WORK, 'tantu-uxtest' + (process.platform === 'win32' ? '.exe' : ''));
  fs.mkdirSync(store, { recursive: true }); fs.mkdirSync(out, { recursive: true });
  if (WITH_PEERS) seedPeers(store);

  let hub = null, browser = null, context = null, page = null;
  // Ports must be free before the Hub starts, or an orphan answers every
  // probe and a cascade of product failures follows. See the call site.
  //
  // A raw TCP connect, deliberately, rather than an HTTP GET. Three reasons,
  // and the second one is a false positive that would stop every run on a
  // machine that has it:
  //
  //   - P2P_PORT speaks TLS, so there is no HTTP to speak to it at all. What is
  //     being detected is "is there a listener", not "does it understand me".
  //   - `fetch` honours the HTTP proxy environment. With NODE_USE_ENV_PROXY
  //     set -- and this repository's own NO_PROXY already had to be written to
  //     keep 127.0.0.1 out of a proxy -- a request can be answered by a proxy on
  //     another host while the local port is free, and the run would refuse to
  //     start on a perfectly good machine, for ever.
  //   - `net.connect` cannot be redirected by anything. The answer it gives is
  //     the one the question asks.
  //
  // node:net is the one thing here that needs no new import: it is used by the
  // browser plumbing already, and adding it is cheaper than a wrong answer.
  function portIsListening(port, timeoutMs = 1500) {
    return new Promise((resolve) => {
      const socket = net.connect({ port, host: '127.0.0.1' });
      const done = (busy) => {
        socket.removeAllListeners();
        socket.destroy();
        resolve(busy);
      };
      socket.setTimeout(timeoutMs, () => done(false));
      socket.once('connect', () => done(true));
      socket.once('error', () => done(false));
    });
  }

  async function assertPortsFree() {
    const busy = [];
    for (const port of [WEB_PORT, P2P_PORT]) {
      if (await portIsListening(port)) busy.push(port);
    }
    if (busy.length) {
      throw new Error('port(s) ' + busy.join(', ') + ' are already in use before this run started.\n'
        + 'An earlier harness run was interrupted and left its Hub behind; it answers every probe, '
        + 'and the web-UI port fallback means this run\'s Hub will move to another port and stay alive, '
        + 'so the run continues against a Hub that is not the one it built.\n'
        + 'Stop it and re-run:  taskkill /F /IM tantu-uxtest.exe   (or kill the process holding the port)');
    }
  }
// The extra Hubs the --two-hub scenario spawns.
//
// Declared here, beside the other cleanup-visible handles, and not inside the
// scenario block. It was declared in the block and read in the finally clause,
// which is a different scope: every run ended in a ReferenceError, so the rest
// of the finally never executed. That did not merely print a stack trace -- it
// meant the Hub under test was never killed and the work directory was never
// deleted, on every single run, and the ReferenceError replaced an already-
// decided exit code with 1. A passing run reported failure.
let hubs2 = null;
  try {
    // The ports must be free *before* the Hub starts, and saying so up front is
  // the only way to tell this apart from a broken product.
  //
  // A leftover Hub from an interrupted run holds WEB_PORT. Our Hub then cannot
  // bind it -- and it does not give up: the web UI falls back to the next port
  // in its scan, so it stays alive and every readiness condition passes. The
  // probe below is answered by the *orphan*, and the version-tag check is no
  // help, because the orphan was built from the same commit and the tag
  // matches.
  //
  // What follows is a cascade in four vocabulations: no render stamp, no
  // session, 403 on /api/session, no pairing banner, an inert bookmarklet,
  // "Dashboard session not established" for a send. It reads as the product
  // having broken in eleven places at once, and on 2026-10-18 it did exactly
  // that, twice, before the cause -- one orphaned process -- was found.
  //
  // Exit 3 is already reserved for "the harness itself failed", which is what
  // this is. A precondition that is not met must stop the run here, not turn
  // into twenty product failures somewhere in the middle of it.
  //
  // Before the build, deliberately: this run's own Hub is a fixed cost of a few
  // seconds and produces nothing the reader needs when the run cannot happen.
  await assertPortsFree();

  console.log('uxtest: building');
    go(['build', '-o', exe, './cmd/tantu']);
    const head = git(['rev-parse', '--short', 'HEAD']).trim();
    console.log('uxtest: HEAD ' + head + (WITH_PEERS ? ' (3 seeded peers)' : ' (peerless)') + ', engine ' + ENGINE);

    hub = spawn(exe, ['hub', '--server', '--transport', 'loopback',
      '--web-addr', '127.0.0.1:' + WEB_PORT, '--listen', '127.0.0.1:' + P2P_PORT,
      '--store-dir', store, '--output-dir', out], { stdio: ['ignore', 'pipe', 'pipe'] });

    // The Hub prints its banner only after its listeners are bound, which on a
    // cold machine takes several seconds. A fixed sleep raced it, and because
    // stdout/stderr were discarded a failed start surfaced as a bare
    // "fetch failed" â€” indistinguishable from a slow machine, a crashed Hub,
    // or another Tantu already holding one of these ports. Probe until it
    // answers, and if it never does, say exactly what happened.
    let hubOut = '';
    hub.stdout.on('data', (d) => { hubOut += d; lastHubOut = hubOut; });
    hub.stderr.on('data', (d) => { hubOut += d; lastHubOut = hubOut; });
    // Registered before the readiness probe so a Hub that dies during startup
    // is classified rather than merely reported.
    hub.on('exit', (code, signal) => {
      if (hubDeath === null) {
        hubDeath = 'exit code ' + code + (signal ? ', signal ' + signal : '');
      }
    });

    let probe = null, lastProbeError = '';
    const readyBy = Date.now() + 30000;
    while (Date.now() < readyBy && hub.exitCode === null && !probe) {
      try {
        probe = await (await fetch('http://127.0.0.1:' + WEB_PORT + '/')).text();
      } catch (e) {
        lastProbeError = e.message;
        await sleep(250);
      }
    }
    if (hub.exitCode !== null) {
      throw new Error('Hub exited with code ' + hub.exitCode + ' before answering on ' + WEB_PORT + '.\n'
        + (hubOut.trim() || '(no output)')
        + '\nAnother Tantu holding ' + WEB_PORT + ' or ' + P2P_PORT + ' is the usual cause â€” this harness needs both ports free.');
    }
    if (!probe) {
      throw new Error('Hub did not answer on ' + WEB_PORT + ' within 30s (' + lastProbeError + ').\n'
        + (hubOut.trim() || '(no output)'));
    }
    const vtag = (probe.match(/class="version-tag">([^<]*)</) || [])[1] || '';
    if (!vtag.includes(head)) throw new Error('served tag ' + vtag + ' does not contain HEAD ' + head);
    console.log('uxtest: serving ' + vtag);

    const link = execFileSync(exe, ['dashboard', '--store-dir', store, '--print'], { cwd: WORK, encoding: 'utf8' });
    const token = (String(link).match(/tantu_bootstrap=([a-f0-9]+)/) || [])[1];
    if (!token) throw new Error('could not mint a one-time dashboard link');

    // Playwright manages the browser process and profile itself. A system
    // Chrome/Edge can stand in for Playwright's chromium via TANTU_TEST_CHROME
    // (a local run that has not downloaded Playwright's own build); firefox
    // and webkit always use Playwright's builds, because their protocols are
    // not Chrome's.
    const launchOpts = { headless: true };
    if (ENGINE === 'chromium' && process.env.TANTU_TEST_CHROME && !process.env.TANTU_TEST_CHROME.includes('*')) {
      launchOpts.executablePath = process.env.TANTU_TEST_CHROME;
    }
    try {
      browser = await launcher.launch(launchOpts);
    } catch (e) {
      throw new Error('could not launch ' + ENGINE + ': ' + String(e.message).split('\n')[0]
        + '\nInstall it with: cd tools/uxtest && npm ci && npx playwright install ' + ENGINE);
    }
    // 800x600 matches the headless default the previous harness ran on, so
    // checks that predate viewport control see the layout they were proven on.
    context = await browser.newContext({ viewport: { width: 800, height: 600 } });
    page = await context.newPage();

    // First-useful-render timestamp. Runs in every document this page loads,
    // so the final (post-bootstrap) document records the instant its own
    // status slots first hold a real value instead of the "-" placeholder.
    // Only code running inside the document can observe that instant â€”
    // polling from here would measure the harness, not the render.
    await page.addInitScript(() => {
      window.__tantuFirstUsefulMs = undefined;
      // Which document is this? Only the pre-bootstrap document ever sees a
      // fragment token; the one that survives the reload never does. The settle
      // loop waits on this rather than on the hash, because the product strips
      // the fragment and only then reloads (dashboard.html:1686-1689) -- so "the
      // fragment is gone" is true one step before the boot has finished, and on
      // a loaded runner the next read can still land in the old document.
      const m = /tantu_bootstrap=([^&]*)/.exec(location.hash || '');
      window.__tantuBootFragment = m ? m[1] : null;
      window.__tantuDocId = Math.random().toString(36).slice(2, 10);
      let elapsed = 0;
      const probe = () => {
        const el = document.getElementById('idTransport');
        const v = el && el.innerText ? el.innerText.trim() : '';
        if (document.readyState !== 'loading' && v && v !== '-') {
          window.__tantuFirstUsefulMs = Math.round(performance.now());
          return;
        }
        elapsed += 25;
        if (elapsed <= 6000) setTimeout(probe, 25);
      };
      setTimeout(probe, 25);
    });

    // Console hygiene feed: uncaught exceptions and console.error output,
    // replacing CDP's Runtime.exceptionThrown and Log.entryAdded.
    const consoleErrors = [];
    // Native dialogs (alert/confirm/prompt/beforeunload) are a product defect,
    // not harness noise: they are OS-owned, ignore the app's themes, block the
    // page, and tell a screen-reader user nothing actionable. This harness used
    // to *stub* window.alert/prompt/confirm during the action sweep, which is
    // precisely why twenty-odd alert() call sites went unnoticed. Now every
    // native dialog is recorded and dismissed, and the run fails on it.
    const nativeDialogs = [];
    page.on('dialog', async (d) => {
      nativeDialogs.push(d.type() + ': ' + String(d.message()).slice(0, 160));
      try { await d.dismiss(); } catch (_) { /* already handled */ }
    });
    // Set while the staleness check deliberately cuts /api/status: the browser
    // logs each blocked poll as a resource error. That is the harness's own
    // interference, not dashboard noise â€” CDP's setBlockedURLs produced no
    // Log.entryAdded for it either, so excluding it keeps the check's meaning
    // (unexpected console errors) rather than its mechanism.
    let statusBlocked = false;
    // The stack's first frames are kept: a message like WebKit's
    // "â€¦ due to access control checks" names no caller, and the caller is the
    // entire question when an engine disagrees with the other two.
    page.on('pageerror', (e) => consoleErrors.push('pageerror: ' + e.message
      + (e.stack ? ' @ ' + String(e.stack).split('\n').slice(0, 3).join(' | ') : '')));
    page.on('console', (m) => {
      if (m.type() !== 'error') return;
      const loc = (typeof m.location === 'function' ? m.location() : null) || {};
      const entry = 'console: ' + m.text() + (loc.url ? ' @ ' + loc.url : '');
      if (statusBlocked && /\/api\/status/.test(entry)) return;
      noteEngineFailure(entry);
      consoleErrors.push(entry);
    });

    // Every expression below is written for the old glue's contract: a failed
    // evaluation yields null, never a thrown harness error, so a missing
    // element fails its own check instead of aborting the run.
    const js = async (expr) => { try { return await page.evaluate(expr); } catch (_) { return null; } };

    const scheme = (s) => page.emulateMedia({ colorScheme: s });
    const metrics = (w, h) => page.setViewportSize({ width: w, height: h });
    const tab = async (id) => { await js(`document.querySelector('[data-tab="${id}"]').click(); 'ok'`); await sleep(300); };
    const shot = async (n) => { await page.screenshot({ path: path.join(out, n + '.png') }); };

    const CONTRAST = (sel) => js(`(function(sel){
      function A(c){var m=c.match(/[0-9.]+/g); if(!m) return 0; return m.length>3?Number(m[3]):1;}
      function hex(c){return c.match(/[0-9.]+/g).map(Number).slice(0,3);}
      function blend(f,b){var fa=A(f),fr=hex(f),br=hex(b); return [fr[0]*fa+br[0]*(1-fa),fr[1]*fa+br[1]*(1-fa),fr[2]*fa+br[2]*(1-fa)];}
      function effBg(el){var layers=[],e=el;
        while(e){var c=getComputedStyle(e).backgroundColor; if(A(c)>0){layers.push(c); if(A(c)===1) break;} e=e.parentElement;}
        var base=hex(getComputedStyle(document.body).backgroundColor);
        for(var i=layers.length-1;i>=0;i--){var l=layers[i]; base = A(l)===1 ? hex(l) : blend(l,'rgb('+base.map(Math.round).join(',')+')');}
        return base;}
      function lum(c){var a=c.map(function(v){v/=255; return v<=0.03928? v/12.92 : Math.pow((v+0.055)/1.055,2.4);}); return 0.2126*a[0]+0.7152*a[1]+0.0722*a[2];}
      var el=document.querySelector(sel); if(!el) return null;
      var s=getComputedStyle(el); if(s.display==='none') return null;
      // Contrast is a property of painted pixels. An element can pass the
      // display check above and still generate no boxes â€” its ancestor (an
      // inactive tab pane) is display:none â€” and then there is nothing on
      // screen to read. Skipping it is the definition of the measurement, not
      // a concession: it also removes a real false-positive class, because
      // WebKit leaves computed colors inside hidden subtrees stale across a
      // prefers-color-scheme flip and resolves them when the subtree renders.
      // Measuring a hidden element reads a value no user can ever see.
      if(!el.getClientRects().length) return null;
      var bg=effBg(el), l1=lum(hex(s.color)), l2=lum(bg);
      var size=parseFloat(s.fontSize), bold=parseInt(s.fontWeight,10)>=700;
      var need=(size>=24||(size>=18.66&&bold))?3:4.5;
      return {ratio:+(((Math.max(l1,l2)+0.05)/(Math.min(l1,l2)+0.05)).toFixed(2)), need:need, size:size,
        color:s.color, bg:bg.map(Math.round), at:(el.className||el.tagName),
        rootMuted:getComputedStyle(document.documentElement).getPropertyValue('--text-muted').trim()};})(${JSON.stringify(sel)})`);

    // Navigate, then wait for the bootstrap exchange to settle.
    //
    // The wait used to be "location.hash is empty", on the reasoning that "the
    // fragment is gone means the page is done booting". That reasoning is wrong
    // and the product says why: bootstrapDashboard() calls
    // history.replaceState() and only then window.location.reload()
    // (dashboard.html:1686-1689). There is therefore a guaranteed window in
    // which the fragment is gone and the boot is not done, and the live document
    // is the pre-reload one -- which has no session cookie and so correctly
    // renders nothing. Reading the render stamp from it yields nothing, and the
    // failure message then blames the Hub for the harness having read the wrong
    // document. On a fast machine the reload lands before the next 250ms poll;
    // on a loaded CI runner it does not. That is why this appeared on webkit
    // only, roughly one run in ten, and never locally.
    //
    // The fragment identifies the document instead: only the pre-bootstrap
    // document ever sees one. See settle.cjs, and selfcheck-settle.cjs, which
    // drives a browser to hold that condition to account.
    const navWallStart = Date.now();
    await page.goto(`http://127.0.0.1:${WEB_PORT}/#tantu_bootstrap=${token}`);
    const BOOT_STATE = 'JSON.stringify({hash: location.hash, bootFragment: '
        + 'window.__tantuBootFragment === undefined ? "UNREADABLE" : window.__tantuBootFragment, '
        + 'docId: window.__tantuDocId === undefined ? "UNREADABLE" : window.__tantuDocId})';
    let settled = false;
    let bootState = null;
    let settledOn = 'poll budget exhausted';
    // A failed exchange strips the fragment and does NOT reload (the 403 and
    // 503 branches return before window.location.reload()), so there the
    // pre-bootstrap document is terminal and bootSettled() can never be true.
    // Grant a bounded grace period after the fragment empties, then take
    // whatever is live and let the session-banner check below say which case it
    // was. Without this, a failed bootstrap would cost the full poll budget.
    const GRACE_MS = 3000;
    let hashEmptySince = null;
    let torn = 0;
    for (let i = 0; i < 60 && !settled; i++) {
      const raw = await js(BOOT_STATE);
      if (raw === null) { torn++; await sleep(250); continue; }
      try { bootState = JSON.parse(raw); } catch (_) { bootState = null; }
      if (bootSettled(bootState)) {
        settled = true;
        settledOn = 'the post-bootstrap document';
      } else if (bootState && bootState.hash === '') {
        if (hashEmptySince === null) hashEmptySince = Date.now();
        if (Date.now() - hashEmptySince > GRACE_MS) {
          settled = true;
          settledOn = 'the fragment emptied but no reload landed within ' + GRACE_MS
            + 'ms; ' + describeBootState(bootState);
        }
      }
      if (!settled) await sleep(250);
    }
    const bootstrapWallMs = Date.now() - navWallStart;
    await sleep(500);

    // --- performance budgets (plan section 12.1, "user-perceived budgets") ---
    // First useful render under 1 second: the init script above stamped the
    // moment this document's status slots first held a real value, measured
    // from its own navigation start. The bootstrap POST + reload is a separate
    // document, so its wall time is measured here and logged as evidence â€”
    // only the render instant is gated, because the settle loop's 250ms poll
    // granularity is harness latency, not product behaviour.
    const firstUseful = await js('window.__tantuFirstUsefulMs');
    const rendered = typeof firstUseful === 'number' && firstUseful >= 0;
    console.log('uxtest: first useful render '
      + (rendered ? firstUseful + 'ms' : 'not measured')
      + ' (bootstrap hop ' + bootstrapWallMs + 'ms, settled on ' + settledOn
      + (torn ? ', ' + torn + ' read(s) torn down mid-reload' : '') + ')');
    // Two questions, not one, because "render undefinedms" is the answer to
    // neither.
    //
    // The old gate folded them: `typeof firstUseful === 'number' && ... < 1000`.
    // On 2026-10-16 a webkit run reported `FAIL first useful render under 1s
    // render undefinedms, bootstrap 202ms` -- a duration check that had no
    // duration, reported in the vocabulary of a duration. The stamp is set when
    // the status slots first hold a real value, so a Hub that never answered
    // leaves it unset, and the gate then reported the *consequence* as if it were
    // the measurement.
    //
    // Splitting it keeps the property covered: the first check fails when nothing
    // was stamped, which is the real condition, and the budget check says plainly
    // that it has nothing to measure rather than inventing a number for it.
    //
    // The failure message names which document the harness was reading rather
    // than asserting that the Hub withheld data. Those are different faults with
    // different owners, and the old wording could only ever describe the second,
    // which is why a harness race was reported as a product fault. check() caps
    // the detail at 240 characters, so the state goes first.
    check('the document stamps a first-useful-render instant',
      rendered,
      rendered ? 'stamped ' + firstUseful + 'ms'
        : 'settled on ' + settledOn + '; ' + describeBootState(bootState)
          + '; nothing stamped __tantuFirstUsefulMs');
    if (rendered) {
      check('first useful render under 1s',
        firstUseful < 1000,
        'render ' + firstUseful + 'ms, bootstrap ' + bootstrapWallMs + 'ms');
    } else {
      skip('first useful render under 1s',
        'nothing measured: the render instant was never stamped, so there is no duration to compare against a budget. The preceding check fails on that condition itself');
    }

    // --- session and the send preview (the CSP regression) ---
    const sess = await js('(function(){return {banner:getComputedStyle(document.getElementById("sessionBanner")).display, dest:document.getElementById("destinationSummary").textContent, url:location.href};})()');
    check('session established', sess && sess.banner === 'none', JSON.stringify(sess));
    check('destination always visible', sess && /Destination: .+/.test(sess.dest || ''), sess && sess.dest);
    check('one-time token stripped from the URL', sess && !/tantu_bootstrap/.test(sess.url), sess && sess.url);

    // The staging file, built through a Blob and checked before use.
//
// It was built as `new File([bytes], name, {type:'image/png'})` and never
// verified. CI's WebKit run staged a file whose type did not survive, the
// dashboard's preview guard rejected it, `thumb.src` was never assigned -- and
// the harness then sat out a four-second deadline and reported three unrelated
// failures, including "first useful render" with an undefined duration, which
// was collateral damage from the same missing precondition.
//
// A Blob's type is reliably preserved across engines, and the assertions below
// mean that if it ever is not, this fails in one line naming the engine instead
// of three failures four seconds later with no cause.
const staged = await js(`(function(){
      // A 1x1 PNG, written out as bytes rather than decoded from base64.
      //
      // Both of the obvious ways to get these 70 bytes into the page turned out
      // to be environment-dependent. atob produced a zero-byte File on CI's
      // WebKit -- type and isBlob right, size 0, which is exactly what an empty
      // Uint8Array looks like. fetch on a data: URL is refused, and rightly: the
      // dashboard's own CSP blocks data:, which is correct behaviour on the
      // product's part and unusable for the harness.
      //
      // A literal array needs no decoder, no fetch, and no CSP exemption, so it
      // is the same bytes everywhere. The first eight are the PNG signature; if
      // that is ever wrong nothing will decode, and the precondition check below
      // says so with the size it actually got.
      var bytes=[
      137,80,78,71,13,10,26,10,0,0,0,13,73,72,68,82,
      0,0,0,1,0,0,0,1,8,6,0,0,0,31,21,196,
      137,0,0,0,13,73,68,65,84,120,218,99,252,207,192,80,
      15,0,4,133,1,128,132,169,140,33,0,0,0,0,73,69,
      78,68,174,66,96,130
      ];
      var f=new File([new Uint8Array(bytes)],'uxtest-shot.png',{type:'image/png'});
      window.__uxtestStagedFile=f;
      return JSON.stringify({type:f.type, size:f.size, isBlob:(f instanceof Blob),
                             bytes:bytes.length,
                             pngSig:bytes.slice(0,4).join(',')});
    })()`);
const stagedFile = (() => { try { return JSON.parse(staged); } catch (_) { return null; } })();
// The data first, the prose second, and that ordering is deliberate: check() caps
// the detail at 240 characters, and the first version of this message put the
// explanation first, so CI truncated away the only thing that mattered and
// reported a red whose cause was invisible. Put the evidence at the front.
const stagedOK = !!(stagedFile && stagedFile.isBlob && stagedFile.type === 'image/png' && stagedFile.size > 0);
check('the staged preview file is an image this engine will accept', stagedOK,
    JSON.stringify(stagedFile) + ' | the dashboard previews an image only when its type ' +
    'starts with image/ and it is non-empty, so a file this engine built differently turns ' +
    'a one-line precondition failure into a four-second timeout and three unrelated reds');
if (!stagedOK) {
  // Skip, do not fail, the checks that depend on this one. They cannot pass, and
  // a dependent check failing because its precondition failed reports a second
  // fault where there is one -- including "first useful render" with an undefined
  // duration, which has nothing to do with first render. skip() is its own line and
  // its own count in the summary, so this is visibly not-run rather than quietly
  // green and not silently red.
  skip('preview thumbnail decodes (CSP allows blob:)',
       'no file this engine will stage as an image; file was ' + JSON.stringify(stagedFile));
  skip('preview thumbnail is visible',
       'no file this engine will stage as an image; file was ' + JSON.stringify(stagedFile));
  skip('preview names its destination',
       'no file this engine will stage as an image; file was ' + JSON.stringify(stagedFile));
} else {
await js(`stageFileForSend(window.__uxtestStagedFile); 'ok'`);
    // Wait for the fade to settle rather than sampling once. The transition is
    // 250ms, so a single read at 1200ms cannot legitimately catch it mid-fade
    // - unless the blob's decode finished late, which is exactly what happens on
    // a cold browser. This check has now cost two runs on that race (once as a
    // documented collateral failure in Batch Z31, once in this batch), so the
    // measurement waits for the state it is asserting, with a bounded deadline:
    // a thumbnail that never becomes visible still resolves to -1 and fails.
    // The wait re-queries the element on every tick and, on timeout, reports what
    // state the image was actually in.
//
// It did neither before, and that is why this check has now failed three times
    // for reasons nobody could name: the first two are recorded in the comment
    // above as "the blob's decode finished late", which is an assumption, and the
    // third arrived on CI as loaded:false, op:"0", nw:0 after the full 4s --
// indistinguishable from a CSP block, a load event that never fired, and an
    // element that is no longer in the document.
//
// Those are three different bugs with three different fixes, and a check that
// cannot tell them apart costs a full CI cycle per occurrence. So the timeout
// path now answers which one it was:
//
//   connected=false  the element left the document; the wait was watching a node
//                    nobody renders any more
//   complete=false   the image never finished loading -- decode stalled, or the
//                    blob was refused before any load could fire
//   complete=true, nw=0
//                    it loaded and failed to decode, which is what a CSP block
//                    on blob: actually looks like, and is what this check's name
//                    claims to be about
//
// The element is re-queried each tick because re-querying is free and it removes
// a whole class of failure that no diagnostic could otherwise explain.
const thumb = await js(`(function(){return new Promise(function(resolve){
      var dest=document.getElementById('filePreviewDest');
      var t0=performance.now();
      (function tick(){
        var t=document.getElementById('filePreviewThumb');
        if(!t){
          resolve({missing:true, settleMs:-1, dest:dest?dest.textContent:''}); return;
        }
        var snapshot=function(settleMs){
          return {nw:t.naturalWidth,
                  loaded:t.classList.contains('loaded'),
                  op:String(Number(getComputedStyle(t).opacity)),
                  complete:!!t.complete,
                  connected:t.isConnected,
                  src:(t.currentSrc||t.src||'').slice(0,5),
                  // The state of the staging itself, so a missing src can be
                  // attributed. CI produced complete:true with src:"" -- the
                  // state immediately after removeAttribute('src') and before
                  // thumb.src is assigned -- which narrows it to either "the
                  // staging branch never ran" or "its guard was false", and these
                  // three separate those:
                  //   pending:false      stageFileForSend returned without staging
                  //                        (an upload was already in flight, or the
                  //                        expert immediate-send opt-in is ticked)
                  //   display:'block'    staging ran, so the image guard rejected
                  //                        the file -- wrong MIME type, or a size
                  //                        outside the 8 MiB preview ceiling
                  //   display:'none'     staging never reached the image branch
                  pending:(typeof pendingFile!=='undefined') ? (pendingFile!==null) : 'unavailable',
                  display:t.style.display,
                  expert:(function(){var x=document.getElementById('expertImmediateSend');
                                    return x?String(x.checked):'no-box';})(),
                  settleMs:settleMs,
                  dest:dest?dest.textContent:''};
        };
        if(Number(getComputedStyle(t).opacity)>=0.99){ resolve(snapshot(Math.round(performance.now()-t0))); return; }
        if(performance.now()-t0>4000){ resolve(snapshot(-1)); return; }
        setTimeout(tick,25);
      })();
    });})()`);
    console.log('uxtest: preview thumbnail settled in ' + (thumb && thumb.settleMs) + 'ms');
    // Two failures rather than one, because they are two different claims and the
    // difference between them is the diagnosis: a decode failure and a load that
    // never completed have different causes and different fixes, and merging them
    // is what made this check expensive three times.
    check('preview thumbnail decodes (CSP allows blob:)',
      thumb && !thumb.missing && thumb.nw > 0,
      thumb && thumb.missing ? 'the preview element is not in the document' : JSON.stringify(thumb));
    check('preview thumbnail is visible',
      thumb && !thumb.missing && thumb.loaded && Number(thumb.op) > 0.9 && thumb.settleMs >= 0,
      JSON.stringify(thumb));
    check('preview names its destination', thumb && /Destination: /.test(thumb.dest || ''), thumb && thumb.dest);
    await js('cancelPendingSend(); "ok"');
    } // end of: only when this engine produced a file the dashboard will preview

    // --- paste/drop preview feedback under 100ms (plan section 12.1) ---
    // Dispatches a real 'paste' event carrying an image file â€” the path the
    // dashboard documents ("pasting an image anywhere stages it") â€” and
    // measures until the preview card is shown, which stageFileForSend does
    // synchronously: the budget is that the user's paste is acknowledged with
    // visible feedback inside 100ms, not that the image decodes. Engines that
    // cannot construct a ClipboardEvent carrying DataTransfer fall back to
    // calling the same product function directly, and the detail says which
    // path was measured rather than pretending the event worked.
    const pasteFb = await js(`(function(){return new Promise(function(resolve){
      var card=document.getElementById('filePreviewCard');
      if(!card || !card.hidden){ resolve({ms:-1, why:'preview card not reset before the test'}); return; }
      var png='iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';
      var bin=atob(png), arr=new Uint8Array(bin.length); for(var i=0;i<bin.length;i++) arr[i]=bin.charCodeAt(i);
      var t0=performance.now(), path='stage-fn fallback';
      try{
        var dt=new DataTransfer();
        dt.items.add(new File([arr],'uxtest-paste.png',{type:'image/png'}));
        var ev=new ClipboardEvent('paste',{clipboardData:dt,bubbles:true,cancelable:true});
        if(ev.clipboardData && ev.clipboardData.files && ev.clipboardData.files.length===1){
          document.dispatchEvent(ev); path='paste-event';
        } else { throw new Error('clipboardData not carried'); }
      }catch(e){ stageFileForSend(new File([arr],'uxtest-paste.png',{type:'image/png'})); }
      (function tick(){
        if(!card.hidden){ resolve({ms:Math.round(performance.now()-t0), path:path}); return; }
        if(performance.now()-t0>3000){ resolve({ms:-1, path:path, why:'preview never appeared'}); return; }
        setTimeout(tick,8);
      })();
    });})()`);
    console.log('uxtest: paste/drop preview feedback ' + (pasteFb && pasteFb.ms) + 'ms via ' + (pasteFb && pasteFb.path));
    check('paste/drop preview feedback under 100ms',
      pasteFb && pasteFb.ms >= 0 && pasteFb.ms < 100, JSON.stringify(pasteFb));
    await js('cancelPendingSend(); "ok"');

    // --- ordinary UI freeze never over 100ms during state updates (plan
    // section 12.1) --- Sample the main thread for 4s: that window spans at
    // least one status poll and the periodic loads, i.e. real state updates.
    // A gap between timer ticks is exactly what a freeze is â€” the loop cannot
    // run while the thread is blocked â€” so the longest gap is the longest
    // freeze the user would have seen.
    const freeze = await js(`(function(){return new Promise(function(resolve){
      var gaps=[], last=performance.now(), start=last, n=0;
      (function tick(){
        var now=performance.now();
        if(n>0) gaps.push(now-last);
        last=now; n++;
        if(now-start<4000){ setTimeout(tick,16); return; }
        var max=0; for(var i=0;i<gaps.length;i++) if(gaps[i]>max) max=gaps[i];
        resolve({max:Math.round(max), samples:gaps.length});
      })();
    });})()`);
    console.log('uxtest: worst UI freeze ' + (freeze && freeze.max) + 'ms over ' + (freeze && freeze.samples) + ' samples');
    check('no UI freeze over 100ms during state updates',
      freeze && typeof freeze.max === 'number' && freeze.max < 100, JSON.stringify(freeze));

    // --- text composer reports its outcome inline ---
    await js('(function(){document.getElementById("textPayload").value="uxtest";document.querySelector("[data-action=\'send-text\']").click();return "ok";})()');
    await sleep(2500);
    const textRes = await js('(function(){var s=document.getElementById("dropStatus");return {d:getComputedStyle(s).display,t:s.textContent,c:s.className};})()');
    const okOrHonest = textRes && textRes.d !== 'none' && (/sent to/i.test(textRes.t) || /not sent|could not reach/i.test(textRes.t));
    check('text send reports an outcome on this tab', okOrHonest, JSON.stringify(textRes));
    await js('(function(){document.getElementById("textPayload").value="q".repeat(11*1024*1024);document.querySelector("[data-action=\'send-text\']").click();return "ok";})()');
    await sleep(1500);
    const over = await js('(function(){var s=document.getElementById("dropStatus");return {d:getComputedStyle(s).display,t:s.textContent,kept:document.getElementById("textPayload").value.length>1000000};})()');
    check('oversize text warns inline and keeps the draft', over && over.d !== 'none' && /too large|limit/i.test(over.t) && over.kept, JSON.stringify(over));
    await js('document.getElementById("textPayload").value=""; "ok"');

    // --- structure: headings, landmarks, bypass, log console ---
    await tab('tab-logs');
    const struct = await js(`(function(){
      var c=document.getElementById('logConsole'); c.focus();
      return {h1:document.querySelectorAll('h1').length, divTitles:document.querySelectorAll('div.card-title').length,
        nav:document.querySelectorAll('nav').length, skip:document.querySelectorAll('a.skip-link').length,
        mainTab:(document.getElementById('main')||{}).tabIndex,
        logRole:c.getAttribute('role'), logName:c.getAttribute('aria-label'), logFocus:document.activeElement===c,
        chromeH:getComputedStyle(document.documentElement).getPropertyValue('--chrome-h').trim()};})()`);
    check('exactly one h1', struct && struct.h1 === 1, JSON.stringify(struct));
    check('no card title is a div', struct && struct.divTitles === 0, JSON.stringify(struct));
    check('navigation landmark and skip link present', struct && struct.nav >= 1 && struct.skip === 1, JSON.stringify(struct));
    check('main is a skip target', struct && struct.mainTab === -1, JSON.stringify(struct));
    check('log console is a named, focusable log region', struct && struct.logRole === 'log' && !!struct.logName && struct.logFocus, JSON.stringify(struct));
    check('sticky chrome height is measured', struct && /^\d+px$/.test(struct.chromeH || ''), struct && struct.chromeH);

    // --- pairing approval keeps focus and is announced ---
    const pairing = await js(`(function(){
      var b=document.getElementById('pendingPairingsBanner');
      var role=b.getAttribute('role'), live=b.getAttribute('aria-live');
      var real=window.fetch;
      window.fetch=function(){return Promise.resolve({ok:true,status:200,json:function(){return Promise.resolve([{id:'r1',peer_name:'Box',remote_addr:'10.0.0.9:9877',peer_sas:'zz9911'}]);}});};
      return loadPendingPairings().then(function(){
        var b1=document.getElementById('pendingPairingsList').querySelector('button[data-accept="true"]');
        b1.focus(); var held=document.activeElement===b1;
        return loadPendingPairings().then(function(){
          var b2=document.getElementById('pendingPairingsList').querySelector('button[data-accept="true"]');
          var h=Math.round(b2.getBoundingClientRect().height);
          window.fetch=real;
          document.getElementById('pendingPairingsBanner').style.display='none';
          document.getElementById('pendingPairingsList').innerHTML='';
          return {role:role, live:live, held:held, same:(b1===b2), h:h};});});})()`);
    check('pairing banner is an announced status region', pairing && pairing.role === 'status' && pairing.live === 'polite', JSON.stringify(pairing));
    check('pairing approval keeps focus across polls', pairing && pairing.held && pairing.same, JSON.stringify(pairing));
    check('pairing approval meets 44px', pairing && pairing.h >= 44, pairing && ('h=' + pairing.h));

    // --- the pairing decision request, end to end ---
    //
    // The pending list is synthetic (a real one needs a second Hub initiating),
    // but the decision goes through the product's real code: the consequence
    // dialog, decidePairing, apiFetch and the JSON body. Only the network
    // boundary is intercepted, so what is asserted below is what the product
    // writes on the wire.
    const decisions = [];
    await context.route('**/api/pair/pending', (route) => route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify([{ id: 'req-7', peer_name: 'Box', remote_addr: '10.0.0.9:9877', peer_sas: 'zz9911' }])
    }));
    await context.route('**/api/pair/decision', (route) => {
      const r = route.request();
      decisions.push({
        method: r.method(),
        url: r.url(),
        contentType: (r.headers()['content-type'] || ''),
        raw: r.postData()
      });
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ status: 'success' })
      });
    });

    const drive = async (accept) => {
      await js('loadPendingPairings(); "ok"');
      await sleep(250);
      const opened = await js(`(function(){
        var list = document.getElementById('pendingPairingsList');
        var b = list.querySelector('button[data-accept="${accept}"]');
        if (!b) return { found: false };
        b.click();
        var dlg = document.getElementById('actionDialog');
        return {
          found: true,
          dialogShown: getComputedStyle(dlg).display !== 'none',
          dialogText: (document.getElementById('actionDialogBody').textContent || '').replace(/\\s+/g,' ').trim()
        };
      })()`);
      // Approve goes through a consequence dialog first. Confirming it is part of
      // the path under test, so the harness clicks the real button.
      if (opened && opened.dialogShown) {
        await js('document.getElementById("actionDialogConfirm").click(); "ok"');
      }
      // Always wait, on both branches. The decision is a POST, and a rejection
      // never opens the dialog, so waiting only inside that branch read
      // decisions[] before the request existed. The empty array looked exactly
      // like a product that silently drops rejections, which it does not: the
      // body goes out as {"id":"...","accept":false}, verified by instrumenting
      // fetch directly. Sixth probe to report a confident wrong answer.
      await sleep(400);
      return opened;
    };

    const approveRun = await drive('true');
    const afterApprove = await js(`(function(){
      var region = document.getElementById('toastRegionPolite');
      var t = region.querySelector('.toast');
      return { text: t ? t.textContent.replace(/\\s+/g,' ').trim() : '' };
    })()`);

    const rejectRun = await drive('false');

    check('pairing: an approval asks for confirmation before trusting', !!(approveRun && approveRun.dialogShown),
      JSON.stringify(approveRun));
    check('pairing: the consequence dialog explains what approving grants',
      !!(approveRun && approveRun.dialogText && approveRun.dialogText.length > 20),
      approveRun && approveRun.dialogText);
    check('pairing: approval posts to /api/pair/decision', decisions.length >= 1 &&
      decisions[0].method === 'POST' && /\/api\/pair\/decision$/.test(decisions[0].url),
      JSON.stringify(decisions[0] || null));
    check('pairing: the approval body carries the pairing id and accept:true',
      !!(decisions[0] && decisions[0].raw && JSON.parse(decisions[0].raw).id === 'req-7' &&
         JSON.parse(decisions[0].raw).accept === true),
      decisions[0] && decisions[0].raw);
    check('pairing: the decision is sent as JSON', !!(decisions[0] && /application\/json/.test(decisions[0].contentType)),
      decisions[0] && decisions[0].contentType);
    check('pairing: approval is confirmed to the user in words',
      !!(afterApprove && afterApprove.text && /approved|trusted/i.test(afterApprove.text)),
      JSON.stringify(afterApprove));
    check('pairing: rejecting sends accept:false for the same id',
      !!(decisions[1] && decisions[1].raw && JSON.parse(decisions[1].raw).id === 'req-7' &&
         JSON.parse(decisions[1].raw).accept === false),
      decisions[1] && decisions[1].raw);
    check('pairing: rejection is not routed through the approve dialog', !!(rejectRun && rejectRun.dialogShown === false),
      JSON.stringify(rejectRun));

    await context.unroute('**/api/pair/pending');
    await context.unroute('**/api/pair/decision');
    await js(`(function(){
      document.getElementById('pendingPairingsBanner').style.display='none';
      document.getElementById('pendingPairingsList').innerHTML='';
      return 'ok';
    })()`);

    // --- staleness ---
    await context.route('**/api/status*', (route) => route.abort('failed'));
    statusBlocked = true;
    await sleep(9000);
    const stale = await js('(function(){return {b:document.getElementById("staleBanner").style.display, t:document.getElementById("staleBanner").textContent.replace(/\\s+/g," ").trim(), a:document.getElementById("destinationSummary").getAttribute("data-stale"), dest:document.getElementById("destinationSummary").textContent.length>0};})()');
    check('unreachable Hub raises a staleness banner', stale && stale.b === 'block', JSON.stringify(stale));
    check('banner says last-known and offers a retry', stale && /last known/i.test(stale.t) && /retry/i.test(stale.t), stale && stale.t);
    check('destination is labelled, never hidden', stale && stale.a === 'true' && stale.dest, JSON.stringify(stale));
    // Reconnect snapshot reconciliation under 2s (plan section 12.1).
    // Release the block with NO manual retry: recovery must come from the
    // dashboard's own polling (1s cadence while stale), so what is measured
    // is the product's reconnection, not the button's â€” retry-status stays
    // covered by the delegated-action sweep below. The 9s block leaves the
    // poll phase arbitrary, which is safe in the passing direction (recovery
    // is bounded by the 1s stale cadence, not by phase) while a dashboard
    // with no auto-recovery resolves to -1 on a fixed deadline: the gate
    // cannot pass or fail by luck.
    await context.unroute('**/api/status*');
    const recMs = await js(`(function(){return new Promise(function(resolve){
      var start=performance.now();
      (function tick(){
        if(getComputedStyle(document.getElementById('staleBanner')).display==='none'){ resolve(Math.round(performance.now()-start)); return; }
        if(performance.now()-start>5000){ resolve(-1); return; }
        setTimeout(tick,50);
      })();
    });})()`);
    console.log('uxtest: reconnect snapshot reconciliation ' + recMs + 'ms');
    check('reconnect snapshot reconciliation under 2s',
      typeof recMs === 'number' && recMs >= 0 && recMs < 2000, 'banner cleared in ' + recMs + 'ms');
    const live2 = await js('(function(){return {b:document.getElementById("staleBanner").style.display, a:document.getElementById("destinationSummary").getAttribute("data-stale")};})()');
    check('staleness clears on recovery', live2 && live2.b === 'none' && live2.a === null, JSON.stringify(live2));
    // Only now, with the Hub answering again, does a /api/status console
    // error mean something unexpected â€” so only now is one recorded.
    statusBlocked = false;

    // --- contrast, both themes ---
    const TARGETS = ['#nextActionBanner', '#downloadDirPath', '#idSAS', '#logConsole .log-entry', '#logConsole .log-entry span', '.version-tag', '.pill', '.pill.active', '.stat-val', '.hint-text', '.tag-drop', '.tag-error', '.drop-zone-subtext', '#destinationSummary', '#peerLabel', '.stale-banner'];
    // A failing contrast number is the only thing the loop records, so a
    // sweep that silently measured nothing would still report green: count
    // what it actually measured and assert that below.
    // CONTRAST_MIN_MEASURED is the floor: every rendered target across every
    // tab, in both themes (visibility must not depend on the theme). It is
    // also the canary for the machinery itself â€” if tab activation breaks,
    // whole panes go display:none and the count drops below it.
    //
    // This was 26 and is now 21 for one specific reason, so that nobody has to
    // rediscover it: #nextActionBanner sits in the page header, outside the tab
    // panes, so it used to be measured once per tab â€” five measurements of the
    // same element. It now renders only when it has something to steer, because
    // a banner that is always populated is one nobody reads, and in the common
    // case (a peer is connected, nothing is staged) it correctly stays hidden.
    // That removes exactly those five. Verified by reverting only that one
    // behaviour, which returned the count to 26 with no other change present.
    const CONTRAST_MIN_MEASURED = 21;
    const measured = { dark: 0, light: 0 };
    // Which selectors did not render, per theme. Without this the floor check
    // can only say "21 of 26" and never which five went missing, so a drop is
    // only diagnosable by bisecting the dashboard by hand.
    const notRendered = { dark: [], light: [] };
    for (const s of ['dark', 'light']) {
      await scheme(s); await sleep(200);
      for (const t of ['tab-drop', 'tab-relay', 'tab-peers', 'tab-transfers', 'tab-logs']) {
        await tab(t);
        for (const sel of TARGETS) {
          const v = await CONTRAST(sel);
          if (v) measured[s]++; else notRendered[s].push(t + ' ' + sel);
          if (v && v.ratio < v.need) {
            // A failing contrast number alone says what happened, not why:
            // carry the element's ancestry and every matching color rule, so
            // a failure is diagnosable from its log line.
            const deep = await js(`(function(sel){
              var el=document.querySelector(sel); if(!el) return null;
              var chain=[], e=el, n=0;
              while(e && n<6){var s=getComputedStyle(e); chain.push(String(e.tagName)+'.'+String(e.className||'').split(' ')[0]+' color='+s.color+' bg='+s.backgroundColor); e=e.parentElement; n++;}
              var rules=[];
              for (var i=0;i<document.styleSheets.length;i++){var sh=document.styleSheets[i];
                try{for(var j=0;j<sh.cssRules.length;j++){var r=sh.cssRules[j];
                  if(r.selectorText && r.style && r.style.color){ try{ if(el.matches(r.selectorText)) rules.push(r.selectorText+' => '+r.style.color); }catch(_){} }
                }}catch(_){}}
              return {chain:chain, rules:rules, html:(el.outerHTML||'').slice(0,160)};})(${JSON.stringify(sel)})`);
            console.log('  deep  ' + JSON.stringify(deep));
            check('contrast ' + s + ' ' + sel, false, JSON.stringify(v) + ' deep=' + JSON.stringify(deep));
          }
        }
      }
    }
    // Coverage: the loop only records failures, so a sweep that measured
    // nothing (or measured different sets per theme, meaning visibility
    // flipped with the theme) would otherwise pass unnoticed. Theme flips
    // must not change what is on screen, and the floor catches a wholesale
    // skip â€” including a regression in the rendered-only rule above.
    check('contrast sweep saw the same rendered targets in both themes',
      measured.dark === measured.light, JSON.stringify(measured));
    check('contrast sweep measured a full complement of rendered targets',
      measured.light >= CONTRAST_MIN_MEASURED,
      JSON.stringify(measured) + ' (floor ' + CONTRAST_MIN_MEASURED + ') missing: ' +
      JSON.stringify(notRendered.light));
    console.log('uxtest: contrast measured ' + JSON.stringify(measured));
    await scheme('dark');

    // --- contrast sweep: every rendered text node, both themes ---
    //
    // The allowlist above is a list of selectors somebody remembered to write
    // down, and it is why six light-theme defects shipped green: none of them
    // was on the list. This walks every visible element that owns a text node,
    // composites each translucent background layer down onto the page, and
    // keeps the worst reading per element.
    //
    // Two things it has to handle that a fixed selector list never had to:
    //
    //   Transitions. .tab-btn and .btn-sm both carry `transition: all 0.2s`, so
    //   switching the scheme leaves them reporting the previous theme's colours
    //   for 200ms. Reading immediately returns the old theme and makes a
    //   correct one look broken -- which is exactly what happened when this was
    //   first written by hand. Transitions are disabled for the duration.
    //
    //   Gradient-filled text. The wordmark paints a gradient with
    //   -webkit-text-fill-color: transparent and background-clip: text, so its
    //   `color` is not what anyone sees. Measuring `color` reported 1.03:1 for a
    //   wordmark that is perfectly legible; the gradient stops are the real
    //   foreground, and when the clip is `text` they are what get measured.
    const CONTRAST_SWEEP = `(function(){
      function parse(c){
        var m = String(c).match(/rgba?\\(([^)]+)\\)/);
        if (!m) return null;
        var p = m[1].split(',').map(function(x){ return parseFloat(x); });
        if (p.length < 3) return null;
        return { r:p[0], g:p[1], b:p[2], a: p.length>3 ? p[3] : 1 };
      }
      function over(fg,bg){ var a=fg.a; return { r:fg.r*a+bg.r*(1-a), g:fg.g*a+bg.g*(1-a), b:fg.b*a+bg.b*(1-a), a:1 }; }
      function srgb(c){ c/=255; return c<=0.03928 ? c/12.92 : Math.pow((c+0.055)/1.055, 2.4); }
      function lum(c){ return 0.2126*srgb(c.r)+0.7152*srgb(c.g)+0.0722*srgb(c.b); }
      function ratio(a,b){ var hi=Math.max(a,b), lo=Math.min(a,b); return (hi+0.05)/(lo+0.05); }
      function effBg(el, skipOwnImage){
        var layers=[], n=el;
        while (n) {
          var cs = getComputedStyle(n);
          var c = parse(cs.backgroundColor);
          if (c && c.a > 0) { layers.push(c); if (c.a === 1) break; }
          var img = cs.backgroundImage;
          // When the element's own background paints its own text, it is
          // the foreground, not a backdrop. Feeding it in made fg and bg
          // identical and reported the wordmark at 1:1.
          if (skipOwnImage && n === el) img = 'none';
          if (img && img !== 'none' && /gradient/.test(img)) {
            var stops = img.match(/rgba?\\([^)]+\\)/g) || [];
            for (var i=0;i<stops.length;i++){ var sc = parse(stops[i]); if (sc && sc.a > 0) layers.push(sc); }
          }
          n = n.parentElement;
        }
        var acc = { r:255, g:255, b:255, a:1 };
        for (var k=layers.length-1;k>=0;k--) acc = over(layers[k], acc);
        return acc;
      }
      function ownText(el){
        var s='';
        for (var i=0;i<el.childNodes.length;i++) if (el.childNodes[i].nodeType===3) s += el.childNodes[i].nodeValue;
        return s.replace(/\\s+/g,' ').trim();
      }
      function visible(el){
        var cs = getComputedStyle(el);
        if (cs.display==='none' || cs.visibility==='hidden' || parseFloat(cs.opacity)===0) return false;
        var r = el.getBoundingClientRect();
        return r.width > 0 && r.height > 0;
      }
      var checked = 0, fails = [], seen = {};
      var all = document.querySelectorAll('body *');
      for (var i=0;i<all.length;i++){
        var el = all[i];
        if (!visible(el)) continue;
        var txt = ownText(el);
        if (!txt) continue;
        var cs = getComputedStyle(el);
        var clip = cs.backgroundClip || cs.webkitBackgroundClip;
        var isGrad = (clip === 'text') && /gradient/.test(cs.backgroundImage || '');
        var fgs = [];
        if (isGrad) {
          var stops = cs.backgroundImage.match(/rgba?\\([^)]+\\)/g) || [];
          for (var j=0;j<stops.length;j++){ var sc2 = parse(stops[j]); if (sc2) fgs.push(sc2); }
        } else {
          var fg = parse(cs.color);
          if (fg && fg.a !== 0) fgs.push(fg);
        }
        if (!fgs.length) continue;
        var size = parseFloat(cs.fontSize);
        var bold = (parseInt(cs.fontWeight,10) || 400) >= 700;
        var need = (size >= 24 || (bold && size >= 18.66)) ? 3.0 : 4.5;
        var bg = effBg(el, isGrad);
        var worst = Infinity, wfg = null;
        for (var f=0; f<fgs.length; f++){
          var rr = ratio(lum(over(fgs[f], bg)), lum(bg));
          if (rr < worst) { worst = rr; wfg = fgs[f]; }
        }
        checked++;
        if (worst < need) {
          var key = el.tagName + '|' + (el.id||'') + '|' + String(el.className||'');
          if (seen[key]) continue;
          seen[key] = 1;
          fails.push({
            sel: el.tagName.toLowerCase() + (el.id ? '#'+el.id : '') +
                 (el.className ? '.'+String(el.className).trim().split(/\\s+/).slice(0,2).join('.') : ''),
            text: txt.slice(0, 48),
            ratio: Math.round(worst*100)/100, need: need, px: Math.round(size*10)/10,
            fg: 'rgb('+Math.round(wfg.r)+','+Math.round(wfg.g)+','+Math.round(wfg.b)+')',
            bg: 'rgb('+Math.round(bg.r)+','+Math.round(bg.g)+','+Math.round(bg.b)+')',
            inline: (el.getAttribute('style')||'').slice(0,80) || null
          });
        }
      }
      return { checked: checked, fails: fails };
    })()`;

    // The floor is a canary for the sweep itself. Measured 157 across the five
    // tabs in the light theme with two peers seeded; set well below that so a
    // normal content change cannot trip it, but high enough that a sweep which
    // silently matched nothing -- a broken selector engine, a theme flip that
    // hid every pane -- cannot report success.
    const SWEEP_FLOOR = 80;
    await js("(function(){var s=document.createElement('style');s.id='uxtest-noanim';" +
      "s.textContent='*,*::before,*::after{transition:none !important;animation:none !important}';" +
      "document.head.appendChild(s);return 'ok';})()");
    const swept = { dark: 0, light: 0 };
    const sweepFails = [];
    for (const s of ['dark', 'light']) {
      await scheme(s);
      await sleep(250);
      for (const t of ['tab-drop', 'tab-relay', 'tab-peers', 'tab-transfers', 'tab-logs']) {
        await tab(t);
        const got = await js(CONTRAST_SWEEP);
        if (!got) { sweepFails.push({ theme: s, tab: t, sel: '(sweep returned nothing)' }); continue; }
        swept[s] += got.checked;
        for (const f of got.fails) sweepFails.push(Object.assign({ theme: s, tab: t }, f));
      }
    }
    await js("(function(){var s=document.getElementById('uxtest-noanim'); if(s) s.remove(); return 'ok';})()");

    // No peer in this rig may claim to be reachable.
    //
    // With --peers every seeded peer points at 192.0.2.0/24, which is TEST-NET-1:
    // reserved for documentation and unroutable by design. A probe against one
    // hangs and fails, so "Reachable" on any of these rows would be a lie -- and
    // it is the lie that matters, because reachability is the one thing in the
    // Peers tab a user will act on.
    //
    // "No chip yet" is allowed. The heartbeat's first probe has a five-second
    // delay by design, so at this point in the run the honest answer for most
    // peers is "not probed yet", and a gate that demanded a verdict here would
    // be demanding one the product has not earned.
    // No peer in this rig may claim to be reachable -- and since the harness's own
    // Hub runs --transport loopback, none of them is ever probed at all.
    //
    // Loopback is the same machine by definition, so a heartbeat there would
    // spend a handshake per interval to learn nothing. That makes this rig the
    // wrong place to look for a chip, and it is worth asserting rather than
    // leaving to chance: a check that demanded a reachability verdict from a
    // loopback Hub would either fail forever or tempt someone into making
    // loopback probe, which is worse than having no chip.
    //
    // The positive half lives in the two-Hub section, where the transport really
    // is lan and the peer really is another process.
    await tab('tab-peers');
    const reachNow = await js(`(function () {
      var chips = document.querySelectorAll('#peersList [data-reach]');
      var claims = [];
      for (var i = 0; i < chips.length; i++) {
        if (chips[i].getAttribute('data-reach') === 'ok') {
          claims.push((chips[i].innerText || '').replace(/\\s+/g, ' ').trim().slice(0, 80));
        }
      }
      return { chips: chips.length, claims: claims };
    })()`).catch((e) => ({ error: String(e) }));
    check('peers: a loopback Hub never reports a peer as reachable',
      reachNow && reachNow.chips === 0 && Array.isArray(reachNow.claims) && reachNow.claims.length === 0,
      JSON.stringify(reachNow));
    // One check per failing element, so the log names the element rather than
    // reporting a count the reader has to go and count.
    for (const f of sweepFails) {
      console.log('  fail  contrast sweep ' + f.theme + ' ' + f.sel + '  ' + JSON.stringify(f));
      check('contrast sweep ' + f.theme + ' ' + f.tab + ' ' + f.sel, false, JSON.stringify(f));
    }
    check('contrast sweep: no unreadable text in either theme', sweepFails.length === 0,
      sweepFails.length + ' failing element(s)');
    check('contrast sweep: same text nodes rendered in both themes',
      swept.dark === swept.light, JSON.stringify(swept));
    check('contrast sweep: actually measured the page', swept.light >= SWEEP_FLOOR,
      JSON.stringify(swept) + ' (floor ' + SWEEP_FLOOR + ')');
    console.log('uxtest: full contrast sweep ' + JSON.stringify(swept) + ', ' + sweepFails.length + ' failure(s)');
    await scheme('dark');

    // OVERFLOW_PROBE reports the document's scroll and client widths, and when the
// document is wider than its viewport, the elements responsible.
//
// It measures each element's *text* as well as its border box, and that is not
// a refinement. An unbreakable string does not widen the block that contains it:
// the block's box stays at the container's width and the text simply runs past
// it. A box-only probe therefore reports a clean page while the document is
// genuinely overflowing -- which is precisely what happened here. A probe built
// on border boxes found a 924-wide page entirely clean, because the one culprit
// was a div whose own box was 360 wide. Since the CI-only failure is 21px and
// the plausible cause is a wider fallback font on the runner, a probe that
// cannot see unwrapped text would name nothing on the very platform that
// exhibits the defect.
//
// Only leaf-ish elements have their text measured, so an ancestor does not
// re-report its descendants' overflow and bury them.
//
// The selector list is deliberately shallow -- tag, id, up to three classes --
// because this string goes into a CI log line and a full selector path would
// bury it. The widest offenders are reported first, since the page-level symptom
// is almost always one element, not twenty.
const OVERFLOW_PROBE = `(function(){
  var d = document.documentElement;
  var vw = d.clientWidth, out = [];
  var all = document.querySelectorAll('*');
  var range = document.createRange();

  // True when some ancestor scrolls horizontally, which means an element inside
  // it being wider than the viewport is by design -- a scrollable tab strip, not
  // a broken page. Those are reported, because "is it broken" and "is it the
  // cause" are different questions, but they are ranked last.
  //
  // Ranking them first is what buried the real defect. The narrow-viewport
  // failure was a send button's label running 21px past its own box, while three
  // off-screen tab buttons in a scroll container reported 196px to 501px of
  // overflow and occupied every slot in the log.
  function inScroller(el) {
    for (var p = el.parentElement; p; p = p.parentElement) {
      var ox = getComputedStyle(p).overflowX;
      if (ox === 'auto' || ox === 'scroll' || ox === 'hidden') return true;
    }
    return false;
  }

  for (var i = 0; i < all.length; i++) {
    var el = all[i], r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) continue;
    var over = Math.max(r.right - vw, r.width - vw);
    var why = over > 0.5 ? 'box' : '';
    if (el.children.length === 0) {
      var tr = null;
      try { range.selectNodeContents(el); tr = range.getBoundingClientRect(); } catch (e) { tr = null; }
      if (tr && tr.width) {
        var textOver = Math.max(tr.right - vw, tr.width - vw);
        if (textOver > over) { over = textOver; why = 'text'; }
      }
    }
    if (over <= 0.5) continue;
    var cls = (typeof el.className === 'string' && el.className.trim())
      ? '.' + el.className.trim().split(/\\s+/).slice(0, 3).join('.') : '';
    out.push({
      sel: el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') + cls,
      why: why, inScroller: inScroller(el), over: Math.round(over), w: Math.round(r.width),
      left: Math.round(r.left), right: Math.round(r.right),
      text: (el.textContent || '').trim().replace(/\\s+/g, ' ').slice(0, 48),
    });
  }
  out.sort(function (a, b) {
    if (a.inScroller !== b.inScroller) return a.inScroller ? 1 : -1;
    return b.over - a.over;
  });
  return { w: d.scrollWidth, c: vw, culprits: out.slice(0, 8) };
})()`;

// --- 200% zoom, narrow viewport, reduced motion ---
    await tab('tab-drop');
    await metrics(640, 450); await sleep(400);
    const zoom = await js('(function(){var d=document.documentElement;return {h:d.scrollWidth>d.clientWidth};})()');
    check('200% zoom: no horizontal scrolling', !zoom.h, JSON.stringify(zoom));
    await shot('zoom200');
    await metrics(360, 780); await sleep(400);
    for (const [t, label] of [['tab-drop', 'QuickDrop'], ['tab-relay', 'Relay'], ['tab-peers', 'Peers'], ['tab-transfers', 'Transfers'], ['tab-logs', 'Logs']]) {
      await tab(t);
      // On overflow, name the elements responsible rather than only reporting
      // the two numbers.
      //
      // CI reported `{"w":381,"c":360}` for Firefox and WebKit while Chromium
      // passed, and the run was reproducible across two engines but not locally:
      // the same harness on this machine passes on all three. "381 is more than
      // 360" is not actionable -- it does not say what is 381 wide. A separate
      // probe that loaded at 360px, then 1280->360, then 1280->640->360 all
      // reported the page clean, which is four different arrangements agreeing
      // with each other and disagreeing with CI. Rather than pick the most
      // plausible arrangement and declare the defect unreproducible, the check
      // now carries its own diagnosis into the log, where the next run on the
      // failing platform will name the element.
      const n = await js(OVERFLOW_PROBE);
      check('360px ' + label + ': no page overflow', n.w <= n.c, JSON.stringify(n));
      if (n.w > n.c && n.culprits && n.culprits.length) {
        for (const c of n.culprits.slice(0, 5)) {
          console.log('uxtest: overflow culprit ' + label + ' ' + JSON.stringify(c));
        }
      }
    }

    // The Relay tab is the product's most distinctive feature and until now the
    // harness only clicked into it. Nothing drove the forwarder or looked at the
    // bookmarklet, so the states added in Z46 -- the primary/fallback badges and
    // the unavailable marking -- had never been rendered in any browser. A Go gate
    // asserting on substrings cannot tell you the pill looks live, and "looks
    // live but does nothing" is exactly what Z46 fixed.
    await tab('tab-relay');
    const relayUI = await js(`(function(){
      var a = document.querySelector('.bookmarklet-btn');
      var box = document.querySelector('.bookmarklet-box');
      var note = document.querySelector('.bookmarklet-unavailable-note');
      var primary = document.querySelector('.path-label--primary');
      var fallback = document.querySelector('.path-label--fallback');
      function shown(el){ if(!el) return false; var s=getComputedStyle(el); return s.display!=='none' && s.visibility!=='hidden'; }
      var cards = [].slice.call(document.querySelectorAll('.card-title'));
      var forwarder = null;
      for (var i=0;i<cards.length;i++){ if(/Forwarder/i.test(cards[i].textContent)) forwarder = cards[i].textContent.trim(); }
      return {
        href: a ? a.getAttribute('href') : null,
        pillShown: shown(a),
        unavailable: box ? box.className.indexOf('unavailable') >= 0 : null,
        noteShown: shown(note),
        primaryText: primary ? primary.textContent.trim() : null,
        fallbackText: fallback ? fallback.textContent.trim() : null,
        hasInput: !!document.getElementById('oauthUrlInput'),
        hasSubmit: !!document.getElementById('btnRelay'),
        forwarderLabel: forwarder
      };
    })()`);
    check('relay: the bookmarklet is present and visible', relayUI && relayUI.pillShown, JSON.stringify(relayUI));
    check('relay: the bookmarklet is a real javascript: URL, not an inert void(0)',
      relayUI && /^javascript:/.test(relayUI.href || '') && relayUI.href !== 'javascript:void(0)',
      'href=' + (relayUI ? relayUI.href : null));
    check('relay: the bookmarklet is marked as the recommended path',
      relayUI && relayUI.primaryText && /one click/i.test(relayUI.primaryText),
      'primary=' + (relayUI ? relayUI.primaryText : null));
    check('relay: the manual forwarder is marked as the fallback',
      relayUI && relayUI.fallbackText && /fallback/i.test(relayUI.fallbackText),
      'fallback=' + (relayUI ? relayUI.fallbackText : null));
    check('relay: the fallback forwarder is still offered beside it',
      relayUI && relayUI.hasInput && relayUI.hasSubmit && !!relayUI.forwarderLabel,
      JSON.stringify(relayUI));
    // The two are asserted together on purpose. A live session must give a
    // working bookmark AND no "unavailable" note; checking either alone passes
    // against the exact defect Z46 fixed, where the pill looked live and did
    // nothing.
    check('relay: a live session shows a working bookmark and no unavailable note',
      relayUI && relayUI.unavailable === false && relayUI.noteShown === false,
      JSON.stringify(relayUI));

    // Row 2.4: which clipboard shapes a browser exposes is now measured, on three
    // engines, per push. The handler used to read only clipboardData.files, so a
    // screenshot pasted on an engine that exposes items instead did nothing at
    // all -- no error, no staged preview, just a swallowed keystroke.
    //
    // These call the shipped extractPastedFiles directly. A Go gate cannot: the
    // logic is JavaScript, and testing it from Go would mean either adding a JS
    // engine (against the pinned dependency surface) or re-implementing it in the
    // test, which passes whenever the real one is broken.
    const clip = await js(`(function(){
      function img(){ return { name:'shot.png', type:'image/png', size:2048 }; }
      var out = [];
      function add(name, data, want){ out.push({name:name, got:extractPastedFiles(data).length, want:want}); }
      add('files shape (copied file)', {files:[img()], items:[{kind:'file',getAsFile:function(){return img();}}]}, 1);
      add('items shape only (screenshot)', {files:[], items:[{kind:'file',getAsFile:function(){return img();}}]}, 1);
      add('files property absent', {items:[{kind:'file',getAsFile:function(){return img();}}]}, 1);
      add('both populated, no duplication', {files:[img(),{name:'a.txt',type:'text/plain',size:9}], items:[{kind:'file',getAsFile:function(){return img();}}]}, 2);
      add('text-only falls through', {files:[], items:[{kind:'string',getAsFile:function(){return null;}}]}, 0);
      add('items with no file kind', {files:[], items:[{kind:'string'},{kind:'string'}]}, 0);
      add('getAsFile throwing is ignored', {files:[], items:[{kind:'file',getAsFile:function(){throw new Error('revoked');}}]}, 0);
      add('one throwing, one good', {files:[], items:[{kind:'file',getAsFile:function(){throw new Error('x');}},{kind:'file',getAsFile:function(){return img();}}]}, 1);
      add('neither property present', {}, 0);
      add('null clipboardData', null, 0);
      return { defined: typeof extractPastedFiles === 'function', rows: out };
    })()`);
    check('clipboard: the extraction helper exists and is callable',
      clip && clip.defined === true, String(clip && clip.defined));
    if (clip && clip.defined && clip.rows) {
      for (const row of clip.rows) {
        check('clipboard shape: ' + row.name, row.got === row.want, 'got ' + row.got + ', want ' + row.want);
      }
    }

    // Measured, not assumed: can this engine carry a DataTransfer on a synthetic
    // ClipboardEvent? Firefox cannot -- it drops clipboardData -- which is why
    // the paste path is not drivable there from a test at all. Discovering that
    // by running is what row 2.4 said was missing, so it is probed and reported
    // rather than assumed either way.
    const clipEventWorks = await js(`(function(){
      try {
        var dt = new DataTransfer();
        dt.items.add(new File([new Uint8Array([1,2,3])], 'probe.bin', {type:'application/octet-stream'}));
        var ev = new ClipboardEvent('paste', {clipboardData:dt, bubbles:true, cancelable:true});
        return !!(ev.clipboardData && ev.clipboardData.files && ev.clipboardData.files.length > 0);
      } catch (e) { return false; }
    })()`);

    if (clipEventWorks) {
      // The behaviour half: a real ClipboardEvent carrying a real DataTransfer
      // must switch to QuickDrop and stage a preview. The shape checks above
      // prove the function; these prove the handler calls it.
      await page.evaluate(`(function(){
        var dt = new DataTransfer();
        var bytes = new Uint8Array([137,80,78,71,13,10,26,10,1,2,3,4,5,6,7,8]);
        dt.items.add(new File([bytes], 'clipboard-shot.png', {type:'image/png'}));
        document.body.focus();
        document.dispatchEvent(new ClipboardEvent('paste', {clipboardData:dt, bubbles:true, cancelable:true}));
      })()`);
      await sleep(800);
      const staged = await js(`(function(){
        var card = document.getElementById('filePreviewCard');
        var meta = document.getElementById('filePreviewMeta');
        var tab = document.getElementById('tab-drop');
        return {
          cardVisible: card ? !card.hasAttribute('hidden') : false,
          meta: meta ? meta.textContent : null,
          onDropTab: tab ? tab.classList.contains('active') : false
        };
      })()`);
      check('clipboard: a pasted image switches to QuickDrop and stages a preview',
        staged && staged.cardVisible === true && staged.onDropTab === true, JSON.stringify(staged));
      check('clipboard: the staged preview names the pasted file and its type',
        staged && /clipboard-shot\.png/.test(staged.meta || '') && /image\/png/.test(staged.meta || ''),
        'meta=' + (staged ? staged.meta : null));
    } else {
      // Not a silent pass. Reported, counted, and recorded in the JSON report.
      skip('clipboard: a pasted image switches to QuickDrop and stages a preview',
        ENGINE + ' drops clipboardData on a synthetic ClipboardEvent, so the paste path cannot be ' +
        'driven from a test here. The extraction shapes above still run: they pass a plain object.');
      skip('clipboard: the staged preview names the pasted file and its type',
        'same cause as the previous skip.');
    }

    // The same tab, reached the way a user reaches it when they type the address
    // into a browser that has never opened this Hub: no bootstrap token and no
    // session cookie, so requestHasDashboardCapability is false.
    //
    // This is the state Z46 changed, and the state the checks above cannot see.
    // Red proof proved it: reverting the server's unavailable marking left all 57
    // checks green, because this harness Hub always holds a session.
    //
    // It must be a *fresh context*. The first attempt navigated the existing page
    // to the bare URL and four checks failed -- correctly, for the wrong reason:
    // the session cookie persisted in the reused context, so the page was still
    // authorised and the banner legitimately did not appear. A private window is
    // the real equivalent, and browser.newContext() is exactly that.
    //
    // A second context also leaves the authenticated page untouched, so no later
    // check depends on recovering from a lost session. An earlier version
    // re-bootstrapped with the same token and asserted the session came back; it
    // cannot, because the token is single-use by design.
    const bareContext = await browser.newContext({ viewport: { width: 1000, height: 800 } });
    const barePage = await bareContext.newPage();
    await barePage.goto(`http://127.0.0.1:${WEB_PORT}/`);
    await sleep(1200);
    const noSession = await barePage.evaluate(`(function(){
      var banner = document.getElementById('sessionBanner');
      var detail = document.getElementById('sessionBannerDetail');
      function shown(el){ if(!el) return false; var s=getComputedStyle(el); return s.display!=='none' && s.visibility!=='hidden'; }
      var box = document.querySelector('.bookmarklet-box');
      var note = document.querySelector('.bookmarklet-unavailable-note');
      var a = document.querySelector('.bookmarklet-btn');
      return {
        bannerShown: shown(banner),
        bannerRole: banner ? banner.getAttribute('role') : null,
        detail: detail ? detail.textContent.trim() : null,
        href: a ? a.getAttribute('href') : null,
        unavailable: box ? box.className.indexOf('unavailable') >= 0 : null,
        noteShown: shown(note)
      };
    })()`).catch(() => null);
    check('no session: the dashboard warns that it is not connected',
      noSession && noSession.bannerShown === true, JSON.stringify(noSession));
    check('no session: the warning is announced, not just drawn',
      noSession && noSession.bannerRole === 'alert', 'role=' + (noSession ? noSession.bannerRole : null));
    check('no session: the recovery instruction names a command that works from here',
      noSession && /tantu dashboard/.test(noSession.detail || ''),
      'detail=' + (noSession ? (noSession.detail || '').slice(0, 120) : null));
    check('no session: the bookmarklet is inert rather than pretending to work',
      noSession && noSession.href === 'javascript:void(0)', 'href=' + (noSession ? noSession.href : null));
    check('no session: the inert bookmarklet is marked unavailable and says why',
      noSession && noSession.unavailable === true && noSession.noteShown === true,
      JSON.stringify(noSession));
    await barePage.screenshot({ path: path.join(out, 'no-session-relay.png') }).catch(() => {});
    await bareContext.close().catch(() => {});


    // --- narrow viewports, two widths, measured ---
    //
    // 640px is the harness default and 360px is a phone. Both are checked
    // because the header broke at one and not the other: at 360px the wordmark,
    // the version tag and the peer pill were forced onto one line and the
    // header came out three rows deep with the pill clipped mid-word at
    // "Devbox (workst". At 640px the same header is one row and truncates
    // cleanly. Nothing caught it because 640px is what the harness looked at,
    // and because the only narrow check that existed was document-level
    // horizontal overflow, which a header that squeezes rather than overflows
    // never triggers.
    //
    // The picture misled me here first: the narrow360 screenshot appeared to
    // have no tab bar at all, and it does -- the shot had simply been taken
    // scrolled past it. Measuring said nav.tabs-nav was at top=95 and visible.
    const NARROW = `(function(){
      function parseBox(sel){
        var el = document.querySelector(sel);
        if (!el) return null;
        var r = el.getBoundingClientRect();
        return { top: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height),
                 visible: r.width > 0 && r.height > 0 };
      }
      var vw = document.documentElement.clientWidth;
      var out = { boxes: {}, overflowing: [], smallTargets: [] };
      ['header', 'nav.tabs-nav', '.tabs-list', '.logo-title', '.version-tag']
        .forEach(function(s){ out.boxes[s] = parseBox(s); });

      var all = document.querySelectorAll('body *');
      for (var i = 0; i < all.length; i++) {
        var el = all[i];
        // The skip link is parked off-screen on purpose; that is how it stays
        // out of the tab order until it is focused.
        if (el.classList && el.classList.contains('skip-link')) continue;
        var cs = getComputedStyle(el);
        if (cs.display === 'none' || cs.visibility === 'hidden') continue;
        var r = el.getBoundingClientRect();
        if (r.width === 0 || r.height === 0) continue;
        if (r.right <= vw + 1 && r.left >= -1) continue;
        var scrollable = false, n = el.parentElement;
        while (n && n !== document.body) {
          if (/auto|scroll/.test(getComputedStyle(n).overflowX)) { scrollable = true; break; }
          n = n.parentElement;
        }
        if (scrollable) continue;
        out.overflowing.push(el.tagName.toLowerCase() +
          (el.id ? '#' + el.id : '') + ' ' + String(el.className || '').slice(0, 24) +
          ' L=' + Math.round(r.left) + ' R=' + Math.round(r.right));
      }

      // WCAG 2.2 SC 2.5.8 asks for 24x24. The number that matters is the hit
      // area, not the control: a checkbox wrapped in a <label> is as big as
      // its label, and measuring the input instead reports 13x13 for a control
      // a user can comfortably tap. That mistake made the probe call the
      // expert immediate-send checkbox a failure on the first run.
      var tap = document.querySelectorAll('a[href], button, input, select, textarea, [role="button"], [role="tab"]');
      for (var j = 0; j < tap.length; j++) {
        var e = tap[j];
        var ecs = getComputedStyle(e);
        if (ecs.display === 'none' || ecs.visibility === 'hidden') continue;
        var er = e.getBoundingClientRect();
        if (er.width === 0 || er.height === 0) continue;
        var target = e, w = er.width, h = er.height;
        var lab = e.closest('label');
        if (lab) {
          var lr = lab.getBoundingClientRect();
          if (lr.width >= w && lr.height >= h) { w = lr.width; h = lr.height; }
        }
        if (w >= 24 && h >= 24) continue;
        out.smallTargets.push((e.id ? '#' + e.id : e.tagName.toLowerCase()) +
          ' ' + Math.round(w) + 'x' + Math.round(h) +
          ' "' + (e.textContent || e.getAttribute('aria-label') || '').replace(/\\s+/g, ' ').trim().slice(0, 24) + '"');
      }
      return out;
    })()`;

    for (const vp of [[360, 780], [640, 900]]) {
      await metrics(vp[0], vp[1]);
      await sleep(350);
      await js('window.scrollTo(0,0); "ok"');
      await sleep(150);
      const p = await js(NARROW);
      const tag = vp[0] + 'px';
      if (!p) {
        check('narrow ' + tag + ': probe ran', false, 'probe returned nothing');
        continue;
      }
      check('narrow ' + tag + ': nothing overflows the viewport', p.overflowing.length === 0,
        JSON.stringify(p.overflowing.slice(0, 6)));
      check('narrow ' + tag + ': every control reaches 24x24 (WCAG 2.5.8)', p.smallTargets.length === 0,
        JSON.stringify(p.smallTargets.slice(0, 8)));
      check('narrow ' + tag + ': the tab bar is rendered', !!(p.boxes['nav.tabs-nav'] && p.boxes['nav.tabs-nav'].visible),
        JSON.stringify(p.boxes['nav.tabs-nav']));
      check('narrow ' + tag + ': the wordmark stays on one line',
        !!(p.boxes['.logo-title'] && p.boxes['.logo-title'].h <= 34), JSON.stringify(p.boxes['.logo-title']));
      // The header may take a second row at 360px; three is the bug. Two rows of
      // content plus padding measures about 95px, and it was 145 before the wrap
      // fix, which is what put the tab bar below the fold.
      check('narrow ' + tag + ': the header does not eat the viewport',
        !!(p.boxes.header && p.boxes.header.h <= 110), JSON.stringify(p.boxes.header));
    }
    await metrics(1280, 900);
    await sleep(200);

    await shot('narrow360');
    await metrics(1280, 900); await sleep(300);
    await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' });
    await sleep(300);
    const motion = await js(`(function(){
      var bad=[];
      document.querySelectorAll('*').forEach(function(e){
        var s=getComputedStyle(e);
        if((parseFloat(s.animationDuration)||0)>0.02 || (parseFloat(s.transitionDuration)||0)>0.02) bad.push(e.tagName);
      });
      return bad.slice(0,8);
    })()`);
    check('reduced motion kills all animation and transition', motion && motion.length === 0, JSON.stringify(motion));

    // --- every delegated action actually runs ---
    // Nothing else in this suite, or anywhere else, clicks these controls.
    // A case that reads its element from the wrong place throws before it does
    // anything: no message, no state change, and a suite that stays green
    // because it never clicked. That is not hypothetical â€” "Cancel sign-in"
    // read its operation id from `this`, which in a listener bound to
    // `document` is `document`, and threw "getAttribute is not a function" on
    // every click while 29/29 checks passed.
    //
    // So sweep every data-action the page can render â€” including the ones only
    // live data produces â€” and fail on any that throws. Dialogs, the OS shell,
    // the clipboard, file choosers and downloads are neutralised; every other
    // call reaches the real Hub, so argument extraction and dispatch are
    // exercised for real rather than against a stub.
    const sweep = await js(`(async function () {
      var errors = [], rejections = [], current = '', restored = [];
      function keep(obj, key, val) { try { restored.push([obj, key, obj[key]]); obj[key] = val; } catch (_) {} }
      window.addEventListener('error', function (e) { errors.push(current + ': ' + e.message); });
      window.addEventListener('unhandledrejection', function (e) {
        rejections.push(current + ': ' + (e.reason && e.reason.message ? e.reason.message : String(e.reason)));
        e.preventDefault();
      });

      // NOTE: window.alert/prompt/confirm are deliberately NOT stubbed. A
      // native dialog is a defect (see the page.on('dialog') guard above), and
      // stubbing them is what let the product ship one on every failure path.
      // Playwright dismisses any that appear, so the sweep still completes and
      // the dedicated "no native dialog opened" check reports them.
      keep(window, 'open', function () { return null; });      // no new windows
      keep(document, 'execCommand', function () { return true; });
      keep(navigator.clipboard || {}, 'writeText', function () { return Promise.resolve(); });
      keep(HTMLInputElement.prototype, 'click', function () {});  // no file chooser
      keep(HTMLAnchorElement.prototype, 'click', function () {}); // no download

      // Reaching the Hub is the point; reaching the OS is not. Opening the
      // downloads folder shells out to the file manager, so answer that one
      // call here. The peer and pairing mutations are answered here too: the
      // sweep synthesises buttons, so it has no real fingerprint or pairing id
      // to send, and those endpoints would answer 400 for a value the product
      // would never submit. Every other call â€” including the cancel this
      // sweep exists to prove â€” reaches the real Hub.
      var realApiFetch = window.apiFetch;
      var syntheticData = /^\\/api\\/(peers\\/(active|default|alias|remove)|pair\\/(decision|initiate))$/;
      var fakeOk = function () {
        return Promise.resolve({ ok: true, status: 200, statusText: 'OK',
          json: function () { return Promise.resolve({ status: 'success' }); } });
      };
      keep(window, 'apiFetch', function (path, options) {
        var p = String(path);
        if (/\\/api\\/open-folder/.test(p)) return fakeOk();
        if (syntheticData.test(p)) return fakeOk();
        return realApiFetch(path, options);
      });

      var names = {}, m;
      var re = /data-action\\s*=\\s*["']([a-z0-9-]+)["']/g;
      var html = document.documentElement.outerHTML;
      while ((m = re.exec(html))) names[m[1]] = true;
      var list = Object.keys(names).sort();

      var host = document.createElement('div');
      host.id = 'uxtest-sweep-host';
      document.body.appendChild(host);
      for (var i = 0; i < list.length; i++) {
        current = list[i];
        var b = document.createElement('button');
        b.type = 'button';
        b.setAttribute('data-action', list[i]);
        b.setAttribute('data-operation-id', 'uxtest-' + list[i]);
        b.setAttribute('data-value', '');
        b.setAttribute('data-address', '127.0.0.1:9877');
        b.setAttribute('data-alias', 'uxtest');
        b.setAttribute('data-name', 'uxtest');
        b.setAttribute('data-accept', 'true');
        host.appendChild(b);
        // An exception thrown inside a listener does not reach this call â€”
        // the browser reports it to window.onerror instead, which is why the
        // error listener above is the actual assertion.
        b.click();
      }
      current = '';
      host.remove();
      await new Promise(function (r) { setTimeout(r, 750); });

      for (var j = 0; j < restored.length; j++) {
        try { restored[j][0][restored[j][1]] = restored[j][2]; } catch (_) {}
      }
      var modal = document.getElementById('pairModal');
      if (modal) modal.style.display = 'none';
      if (typeof closeActionDialog === 'function') closeActionDialog({ confirmed: false });
      var banner = document.getElementById('pendingPairingsBanner');
      if (banner) banner.style.display = 'none';
      var list2 = document.getElementById('pendingPairingsList');
      if (list2) list2.innerHTML = '';
      return { actions: list, errors: errors, rejections: rejections };
    })()`);
    check('action sweep reached every data-action', !!(sweep && sweep.actions && sweep.actions.length >= 30),
      sweep && sweep.actions ? sweep.actions.length + ' actions' : 'sweep returned ' + JSON.stringify(sweep));
    check('every delegated action runs without throwing',
      !!(sweep && (!sweep.errors || sweep.errors.length === 0)),
      JSON.stringify((sweep && sweep.errors) || ['no result']));
    check('no delegated action leaves an unhandled rejection',
      !!(sweep && (!sweep.rejections || sweep.rejections.length === 0)),
      JSON.stringify((sweep && sweep.rejections) || ['no result']));
    check('no native dialog opened', nativeDialogs.length === 0, JSON.stringify(nativeDialogs.slice(0, 3)));
    await sleep(300);

    // --- the in-page confirmation (replaces confirm()) ---
    // Measured, not asserted from source: the modal contract a native dialog
    // does not have is exactly what a screenshot or a keyboard trace can show
    // and a marker test can only claim.
    await tab('tab-logs');
    const confirmFlow = await js(`(async function(){
      var opener = document.querySelector('[data-action="clear-logs"]');
      opener.focus();
      var focusedOpener = document.activeElement === opener;
      opener.click();
      await new Promise(function(r){setTimeout(r,120);});
      var d = document.getElementById('actionDialog');
      var opened = getComputedStyle(d).display !== 'none';
      var role = d.getAttribute('role'), modal = d.getAttribute('aria-modal');
      var titled = d.getAttribute('aria-labelledby') === 'actionDialogTitle'
        && (document.getElementById('actionDialogTitle').textContent || '').trim().length > 0;
      var body = (document.getElementById('actionDialogBody').textContent || '').trim();
      var focusOnConfirm = document.activeElement === document.getElementById('actionDialogConfirm');
      var focusOnCancel = document.activeElement === document.getElementById('actionDialogCancel');
      // Escape must cancel and hand focus back to the control that opened it.
      document.dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true, cancelable:true}));
      await new Promise(function(r){setTimeout(r,120);});
      var closed = getComputedStyle(d).display === 'none';
      var restored = document.activeElement === opener;
      return {opened:opened, role:role, modal:modal, titled:titled, bodyLen:body.length,
              focusOnConfirm:focusOnConfirm, focusOnCancel:focusOnCancel,
              closed:closed, restored:restored, focusedOpener:focusedOpener,
              explains: /own log|transfer and authorization history are untouched/i.test(body)};
    })()`);
    check('confirmation is an in-page modal dialog',
      !!(confirmFlow && confirmFlow.opened && confirmFlow.role === 'dialog' && confirmFlow.modal === 'true' && confirmFlow.titled),
      JSON.stringify(confirmFlow));
    check('confirmation takes focus and names its consequence',
      !!(confirmFlow && confirmFlow.focusOnConfirm && confirmFlow.explains && confirmFlow.bodyLen > 40),
      JSON.stringify(confirmFlow));
    check('Escape cancels and restores focus to the opener',
      !!(confirmFlow && confirmFlow.closed && confirmFlow.restored && confirmFlow.focusedOpener),
      JSON.stringify(confirmFlow));

    // --- the in-page prompt (replaces prompt()) and its wiring to the Hub ---
    // /api/config is intercepted so the rig's output directory is never
    // mutated; what is asserted is that the value typed into the dialog is the
    // value the product sends. The QuickDrop tab is activated first: its
    // controls are display:none on any other tab, and focus() on a hidden
    // control is a no-op, which would make the focus assertions vacuous.
    await tab('tab-drop');
    let configPost = null;
    await context.route('**/api/config', async (route) => {
      if (route.request().method() === 'POST') {
        try { configPost = route.request().postData(); } catch (_) { configPost = ''; }
        await route.fulfill({ status: 200, contentType: 'application/json',
          body: JSON.stringify({ status: 'success', output_dir: 'C:\\uxtest\\out' }) });
        return;
      }
      await route.continue();
    });
    const promptFlow = await js(`(async function(){
      document.querySelector('[data-action="change-download-dir"]').click();
      await new Promise(function(r){setTimeout(r,120);});
      var input = document.getElementById('actionDialogInput');
      var shown = getComputedStyle(input).display !== 'none';
      var focused = document.activeElement === input;
      var labelled = document.getElementById('actionDialogInputLabel').style.display !== 'none';
      input.value = 'C:\\\\uxtest\\\\prompts';
      document.querySelector('#actionDialog [data-action="dialog-confirm"]').click();
      await new Promise(function(r){setTimeout(r,400);});
      return {shown:shown, focused:focused, labelled:labelled,
              closed:getComputedStyle(document.getElementById('actionDialog')).display === 'none'};
    })()`);
    check('prompt is an in-page labelled input',
      !!(promptFlow && promptFlow.shown && promptFlow.focused && promptFlow.labelled && promptFlow.closed),
      JSON.stringify(promptFlow));
    check('prompted value reaches the Hub',
      !!configPost && /uxtest/.test(configPost) && /prompts/.test(configPost),
      'POST /api/config body ' + JSON.stringify(configPost));
    await context.unroute('**/api/config');

    // --- a failure is reported, announced, persistent, and non-blocking ---
    // Answered 200 with an error body rather than 500: it drives the same
    // product branch (the handler reports whenever the body is not a success)
    // without adding a network-level console error to the hygiene check, which
    // would otherwise be asserting against the harness's own injection.
    await context.route('**/api/open-folder', (route) => route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ status: 'error', message: 'uxtest: injected failure' }) }));
    const toastFlow = await js(`(async function(){
      var opener = document.querySelector('[data-action="open-folder"]');
      opener.focus();
      opener.click();
      await new Promise(function(r){setTimeout(r,500);});
      var region = document.getElementById('toastRegionAssertive');
      var t = region.querySelector('.toast');
      var polite = document.getElementById('toastRegionPolite').querySelectorAll('.toast').length;
      var text = t ? t.textContent : '';
      var role = region.getAttribute('role'), live = region.getAttribute('aria-live');
      // An error must not vanish on a timer the user cannot control.
      await new Promise(function(r){setTimeout(r,1200);});
      var still = !!region.querySelector('.toast');
      // Non-modal: the control that failed is still focused, so a keyboard or
      // screen-reader user is not thrown to the top of the document.
      var keptFocus = document.activeElement === opener;
      var dismiss = t && t.querySelector('.toast-dismiss');
      if (dismiss) dismiss.click();
      await new Promise(function(r){setTimeout(r,120);});
      return {has:!!t, text:text, role:role, live:live, polite:polite,
              still:still, keptFocus:keptFocus,
              next:/Next:/.test(text), afterDismiss:!!region.querySelector('.toast'),
              dismissLabelled: !!(dismiss && /dismiss/i.test(dismiss.getAttribute('aria-label') || ''))};
    })()`);
    // A detail is truncated at 240 characters, and this flow captures the whole
    // notification copy in `text`. Leading with the object as-is meant the fields
    // a failing predicate actually reads were the ones cut off: on 2026-10-16 a
    // webkit run reported `an error notification persists until dismissed` with
    // a detail ending in "..." — still, dismissLabelled and afterDismiss were all
    // gone, so the log could not say why it had failed, only that it had.
    //
    // So each check leads with the fields its own predicate reads, and the copy
    // goes last and shortened: it is the longest field and the least diagnostic
    // for a predicate made of booleans.
    const toastDetail = (fields) => {
      if (!toastFlow) return 'no toast flow was captured';
      const o = {};
      for (const f of fields) o[f] = toastFlow[f];
      o.text = String(toastFlow.text || '').replace(/\s+/g, ' ').slice(0, 60);
      return JSON.stringify(o);
    };
    check('a failure is announced as an alert with a next step',
      !!(toastFlow && toastFlow.has && toastFlow.role === 'alert' && toastFlow.live === 'assertive' && toastFlow.next),
      toastDetail(['has', 'role', 'live', 'next']));
    check('an error notification persists until dismissed',
      !!(toastFlow && toastFlow.still && toastFlow.dismissLabelled && !toastFlow.afterDismiss),
      toastDetail(['still', 'dismissLabelled', 'afterDismiss']));
    check('a notification is not modal and does not steal focus',
      !!(toastFlow && toastFlow.keptFocus),
      toastDetail(['keptFocus']));
    await context.unroute('**/api/open-folder');

    // --- an empty list offers a way forward ---
    await tab('tab-transfers');
    await js('renderTransfers([]); "ok"');
    await sleep(150);
    const emptyFlow = await js(`(function(){
      var list = document.getElementById('transfersList');
      var es = list.querySelector('.empty-state');
      var btns = es ? es.querySelectorAll('button[data-action]') : [];
      return {has:!!es, title:es?(es.querySelector('strong')||{}).textContent:'',
              actions:Array.prototype.map.call(btns,function(b){return b.getAttribute('data-action');})};
    })()`);
    check('an empty list offers a next action, not a dead end',
      !!(emptyFlow && emptyFlow.has && emptyFlow.actions.length >= 1),
      JSON.stringify(emptyFlow));

    // ...and the action actually gets you somewhere: it switches to the tab that
    // holds the control, and that control takes focus. A button that navigates
    // without moving focus leaves a keyboard user stranded on a pane they can no
    // longer see.
    const emptyNav = await js(`(function(){
      var btn = document.querySelector('#transfersList .empty-state button[data-action="goto-drop"]');
      if (!btn) return {missing:true};
      btn.click();
      var pane = document.getElementById('tab-drop');
      var ta = document.getElementById('textPayload');
      return {paneVisible:getComputedStyle(pane).display !== 'none',
              tabSelected:document.querySelector('[data-tab="tab-drop"]').getAttribute('aria-selected') === 'true',
              focused:document.activeElement === ta,
              focusedId:document.activeElement ? document.activeElement.id : ''};
    })()`);
    check('the empty-state action navigates and moves focus',
      !!(emptyNav && !emptyNav.missing && emptyNav.paneVisible && emptyNav.tabSelected && emptyNav.focused),
      JSON.stringify(emptyNav));
    await tab('tab-transfers');

    // --- an empty state must survive the poll that re-renders its list ---
    //
    // The three checks above all call a render function directly and read the
    // result. That proves the function produces an empty state; it says nothing
    // about whether the page still *has* one thirty seconds later.
    //
    // Which is how the peers list lost its route forward. The server renders
    // "No paired peers yet" with two buttons -- "+ Pair a device" and "Look for
    // nearby hubs" -- so the first paint is correct, and the Go gate
    // (TestWebDashboard_EmptyStatesOfferANextAction) reads exactly that first
    // paint. renderPeersList, however, assigned innerHTML unconditionally with
    // no empty branch at all, so the first three-second status poll replaced a
    // helpful empty state with an empty <div>. The affordance appeared, and then
    // disappeared, which is the one thing the same gate's comment says it
    // exists to prevent: "the next poll silently removes it".
    //
    // So this check does not call the render function. It waits out a real poll
    // cycle with the page untouched and then asks what the user is looking at.
    // The wait is 3.5s because STATUS_POLL_LIVE_MS is 3000; that is the whole
    // point -- a gate that renders on demand and then asserts the DOM has not
    // changed proves nothing about the timer.
    await js('renderPeersList([]); switchTab("tab-peers"); "ok"');
    await sleep(3500);
    const peersAfterPoll = await js(`(function(){
      var list = document.getElementById('peersList');
      var es = list ? list.querySelector('.empty-state') : null;
      var btns = es ? es.querySelectorAll('button[data-action]') : [];
      return {has:!!es, actions:Array.prototype.map.call(btns,function(b){return b.getAttribute('data-action');})};
    })()`);
    check('the peers empty state survives a status poll',
      !!(peersAfterPoll && peersAfterPoll.has && peersAfterPoll.actions.indexOf('open-pair-modal') !== -1),
      JSON.stringify(peersAfterPoll));

    // --- a suppressed duplicate is not a delivery ---
    //
    // The Hub distinguishes three outcomes on a send and is careful about the
    // one that matters: when the receiver recognises the idempotency key from an
    // earlier completion it writes nothing and answers `duplicate: true` with
    // "Already saved on this peer", and the operation is recorded as
    // duplicate_suppressed. Both send paths ignored that field and rendered
    // their ordinary success line from `destination`, so a retry of a send whose
    // completion acknowledgement was lost told the user "File sent to devbox -
    // verified" for a transfer in which the peer wrote nothing.
    //
    // That is the failure the whole state model is built to avoid -- a
    // completed-looking outcome that is not one -- and it is reachable by
    // re-sending with the same idempotency key, which is the documented way to
    // retry safely. So it is driven here through the real response handler
    // rather than asserted about the markup.
    // Both send paths post to /api/drop/upload -- the text one sends a JSON
    // body where the file one sends multipart -- so one stub covers them, and
    // a second route for a path the page never requests would be decoration.
    await context.route('**/api/drop/upload', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        status: 'success',
        duplicate: true,
        message: 'Already saved on this peer; duplicate delivery suppressed',
        operation_id: 'op-suppressed-1',
        destination: 'devbox',
        verified: true
      })
    }));
    const dupText = await js(`(async function(){
      var ta = document.getElementById('textPayload');
      ta.value = 'a snippet that was already delivered';
      await sendTextDrop();
      var b = document.getElementById('dropStatus');
      return { cls: b.className, text: b.textContent };
    })()`);
    await context.unroute('**/api/drop/upload');
    check('a duplicate the peer suppressed is not reported as sent',
      !!(dupText && !/success/.test(dupText.cls) && !/\bsent\b/.test(dupText.text) &&
         /nothing new was written/i.test(dupText.text)),
      JSON.stringify(dupText));

    // --- tab labels must not wrap ---
    // Fifty checks passed over a nav bar where every one of the five labels had
    // broken onto a second line, because a flex item shrinks below its content
    // width by default and nothing in the CSS said not to. Each button measured
    // 64px tall instead of 46px, so five tabs occupied the space of ten and the
    // section bar was the second-largest element on the page. .tabs-list
    // already scrolls horizontally, which is what narrow screens were always
    // meant to do; this asserts the labels stay on one line so the scroll takes
    // over instead.
    //
    // The viewport is set explicitly because it has to be. An earlier version of
    // this check inherited whatever viewport the previous check left behind --
    // 1280x900, set by the zoom section and never restored -- where the labels
    // have ample room and never wrap, so the check could not fail for the
    // reason it exists. 800px is the harness default and the width at which
    // this was actually observed.
    const tabWrapViewport = page.viewportSize();
    await metrics(800, 600);
    await sleep(250);
    const tabWrap = await js(`(function(){
      var out = [];
      var btns = document.querySelectorAll('.tab-btn');
      for (var i = 0; i < btns.length; i++) {
        var b = btns[i];
        var cs = getComputedStyle(b);
        // Count the rendered lines of the label itself. Padding, borders and a
        // taller emoji glyph all make arithmetic on getBoundingClientRect
        // unreliable -- an earlier version of this check compared the button
        // height against padding + line-height and reported a false wrap on
        // every tab. The text's own client rects are the direct evidence.
        var range = document.createRange();
        range.selectNodeContents(b);
        var rects = Array.prototype.slice.call(range.getClientRects());
        var tops = {};
        for (var j = 0; j < rects.length; j++) {
          if (rects[j].width > 0 || rects[j].height > 0) {
            tops[Math.round(rects[j].top)] = true;
          }
        }
        var lines = Object.keys(tops).length;
        out.push({
          label: b.textContent.trim(),
          lines: lines,
          nowrap: cs.whiteSpace === 'nowrap'
        });
      }
      var nav = document.querySelector('.tabs-nav');
      return {tabs: out, navH: Math.round(nav.getBoundingClientRect().height)};
    })()`);
    const wrappedTabs = (tabWrap && tabWrap.tabs || []).filter(t => t.lines > 1);
    // Only the line count is asserted. An earlier version also bounded the nav
    // height, which cannot work: the nav is 16px of top padding plus a ~46px
    // tab plus a border, so any bound under 63px is unsatisfiable regardless
    // of wrapping. The label line count is the defect itself; the nav height
    // is a consequence of it.
    check('no tab label wraps onto a second line',
      !!(tabWrap && tabWrap.tabs.length >= 5 && wrappedTabs.length === 0 &&
         tabWrap.tabs.every(t => t.nowrap)),
      JSON.stringify(tabWrap));
    // The tab bar scrolls rather than wrapping (Z38). That fixed labels breaking
    // onto a second line, but it moved the problem: at around 800px the five tabs
    // do not fit, so the last one is clipped by the container -- measured at
    // 800px, its right edge sat 66px past the list's. The wrap gate above cannot
    // see this, because it measures button heights and every button is a tidy
    // 46px. A tab that looks cut off reads as a layout bug rather than as "there
    // is more this way".
    //
    // So this asserts the thing that actually matters: that the clipped tab is
    // reachable, and that the page says so. Both halves, because a tab that can
    // be scrolled to but gives no hint is the defect, and a visible shadow over a
    // tab that cannot be reached is a lie.
    const tabReach = await js(`(function(){
      var list = document.querySelector('.tabs-list');
      var last = document.querySelector('[data-tab="tab-logs"]');
      if (!list || !last) return { ok:false, why:'missing' };
      list.scrollLeft = list.scrollWidth;
      var lr = list.getBoundingClientRect();
      var tr = last.getBoundingClientRect();
      var bg = getComputedStyle(list).backgroundImage;
      return {
        overflowing: list.scrollWidth > list.clientWidth,
        reachableOnceScrolled: tr.right <= lr.right + 1,
        hasScrollAffordance: bg && bg !== 'none' && /gradient/.test(bg),
        wasClippedBeforeScroll: true
      };
    })()`);
    check('tab bar: every tab is reachable by scrolling', tabReach && tabReach.reachableOnceScrolled === true,
      JSON.stringify(tabReach));
    check('tab bar: a scrollable bar shows an affordance that there is more',
      tabReach && (tabReach.overflowing === false || tabReach.hasScrollAffordance === true),
      JSON.stringify(tabReach));

    if (tabWrapViewport) { await metrics(tabWrapViewport.width, tabWrapViewport.height); await sleep(200); }

    // --- console hygiene ---
    // Three classes are harness/browser noise, each narrowly scoped:
    // favicon, the javascript: URL the sanitization test deliberately feeds,
    // and 502/503/504 from the intentional oversize upload. The last one is
    // WebKit-specific and documented rather than guessed at: WebKit cancels
    // any fetch issued while a navigation is pending â€” the dashboard's
    // bootstrap reloads the document â€” and mislabels the cancellation as
    // "â€¦ due to access control checks", a CORS phrasing for a request that
    // was never a CORS request (confirmed via a WebKit debug build:
    // supabase/supabase#20982 â€” "canceled because there was a pending
    // navigation â€¦ logged with this 'access control' error"; also SO
    // 63141448, "after reload"). The product's only /api/pair/pending call
    // site is try/caught in full (loadPendingPairings), a real same-origin
    // CORS defect would still be reported by chromium and firefox â€” whose
    // CORS messages are not filtered â€” and the check stays a pageerror-only
    // exception, so any other uncaught exception fails regardless of engine.
    // The pageerror clause reads "the phrase terminates a segment": it was
    // first written as an end-of-entry anchor, which the stack suffix added
    // above defeats â€” a webkit run failed on exactly this entry while the
    // same phrase without a stack was filtered. The suffix alternatives ("
    // @ " before the stack, " | " between its lines, end of entry when there
    // is none) restore the documented scope without widening it: the exact
    // phrasing still has to terminate, so a message that merely mentions
    // "access control checks" mid-sentence still fails.
    const noise = (x) => /favicon\.ico/.test(x) || /javascript%3Aalert/.test(x)
      || (/api\/drop\/upload/.test(x) && /50[023]/.test(x))
      || (/^pageerror: /.test(x) && / due to access control checks\.( @ | \| |$)/.test(x));
    const errs = consoleErrors.filter((x) => !noise(x));
    // The filter decides what counts as noise, so it is under test itself:
    // an anchor that stops matching fails runs on known engine phrasing, and
    // one "fixed" too loosely hides a real CORS defect from the only place it
    // can surface. Samples are built the way the handlers above build them.
    const noiseSamples = [
      // The exact webkit cancellation entry, stack suffix included.
      ['pageerror: /api/pair/pending due to access control checks. @ Fetch API cannot load http://127.0.0.1:9876/api/pair/pending due to access control checks. |     at unknown (http://127.0.0.1:9876/dashboard)', true],
      // The same phrase with no stack (e.stack absent) â€” the shape the
      // filter was originally written against.
      ['pageerror: /api/pair/pending due to access control checks.', true],
      // A real CORS defect in chromium's phrasing must never be filtered.
      ['pageerror: Blocked by CORS policy: No \'Access-Control-Allow-Origin\' header is present on the resource.', false],
      // Any other uncaught exception must never be filtered.
      ['pageerror: Uncaught TypeError: this.getAttribute is not a function', false],
      // The other documented noise classes keep matching.
      ['console: Not found @ http://127.0.0.1:9876/favicon.ico', true],
      ['console: Failed to load resource: the server responded with a status of 503 @ http://127.0.0.1:9876/api/drop/upload', true],
      // An unrelated console error must never be filtered.
      ['console: Unhandled rejection: something else entirely', false],
    ];
    const noiseWrong = noiseSamples.filter(([s, want]) => noise(s) !== want)
      .map(([s, want]) => ({ entry: s.slice(0, 120), want, got: noise(s) }));
    check('console noise filter matches its documented scope', noiseWrong.length === 0, JSON.stringify(noiseWrong));
    check('zero unexpected console errors', errs.length === 0, JSON.stringify(errs.slice(0, 2)));

    // ------------------------------------------------------------ 2.5 budgets
    // --- two real Hubs, paired, with a real transfer between them ---
    //
    // Every "peer" this harness had ever driven was either loopback or the
    // blackhole at 192.0.2.1 that never answers. So the two flows the product
    // most exists for had no browser coverage: a real pairing, and a real
    // transfer that the receiver's inbox actually shows.
    //
    // Self-contained: it spawns its own pair of Hubs on their own ports. The
    // harness's own Hub runs --transport loopback, where IsPeerTrusted is
    // unconditionally true, so a pairing between two loopback Hubs would prove
    // nothing -- and switching that Hub to lan would change the conditions under
    // which the other 104 checks run.
    if (TWO_HUB) {
      const storeA2 = path.join(WORK, 'store2hub-a');
      const storeB = path.join(WORK, 'store2hub-b');
      const outB = path.join(WORK, 'received-by-b');
      fs.mkdirSync(storeA2, { recursive: true });
      fs.mkdirSync(storeB, { recursive: true });
      fs.mkdirSync(outB, { recursive: true });

      const hubOut2 = { a: '', b: '' };
      const spawnHub = (args, label) => {
        const p = spawn(exe, args, { stdio: ['ignore', 'pipe', 'pipe'] });
        p.stdout.on('data', (d) => { hubOut2[label] += d; });
        p.stderr.on('data', (d) => { hubOut2[label] += d; });
        p.on('exit', () => {});
        return p;
      };
      const waitFor = async (port, label) => {
        const by = Date.now() + 30000;
        while (Date.now() < by) {
          try { await (await fetch('http://127.0.0.1:' + port + '/')).text(); return true; } catch (e) {}
          await sleep(250);
        }
        console.log('  note: two-Hub ' + label + ' never answered on ' + port + ': ' + hubOut2[label].slice(-200));
        return false;
      };

      const a2 = spawnHub(['hub', '--auto-pair', '--transport', 'lan',
        '--web-addr', '127.0.0.1:' + WEB_PORT_A2, '--listen', '127.0.0.1:' + P2P_PORT_A2,
        '--store-dir', storeA2, '--output-dir', path.join(WORK, 'sent-by-a2')], 'a');
      const hb = spawnHub(['hub', '--auto-pair', '--transport', 'lan',
        '--web-addr', '127.0.0.1:' + WEB_PORT_B, '--listen', '127.0.0.1:' + P2P_PORT_B,
        '--store-dir', storeB, '--output-dir', outB], 'b');

      const bothUp = (await waitFor(WEB_PORT_A2, 'A2')) && (await waitFor(WEB_PORT_B, 'B'));
      check('two-Hub: both Hubs answer on their own ports', bothUp, '');

      if (bothUp) {
        // Pair them with the product's own CLI. The SAS codes are compared by a
        // human in normal use, so "y" is fed here -- and B runs with
        // --auto-pair, the product's own unattended switch, rather than the
        // harness bypassing the decision endpoint.
        let pairOut = '';
        try {
          pairOut = execFileSync(exe, ['pair', '--store-dir', storeA2,
            '--peer', '127.0.0.1:' + P2P_PORT_B],
            { cwd: WORK, encoding: 'utf8', input: 'y\n' });
        } catch (e) {
          pairOut = String(e.stdout || '') + String(e.stderr || '');
        }
        check('two-Hub: the product CLI pairs the two Hubs',
          /paired/i.test(pairOut) && /SAS:/.test(pairOut),
          pairOut.slice(-220));

        // Both halves converge asynchronously: A's initiate returning says
        // nothing about whether B has recorded A yet. paired_transfer_e2e_test.go
        // records that asserting this immediately passed on Windows and failed on
        // Linux CI, so this polls -- which is not leniency but the only correct
        // assertion for an asynchronous handshake.
        const peerCount = (dir) => {
          try { return Object.keys(JSON.parse(fs.readFileSync(path.join(dir, 'peers.json'), 'utf8'))).length; }
          catch (e) { return 0; }
        };
        let converged = false;
        const byConv = Date.now() + 20000;
        while (Date.now() < byConv) {
          if (peerCount(storeA2) >= 1 && peerCount(storeB) >= 1) { converged = true; break; }
          await sleep(250);
        }
        check('two-Hub: both stores converge on each other',
          converged, 'A=' + peerCount(storeA2) + ' B=' + peerCount(storeB));

        // Each Hub's own dashboard link, minted by its own CLI against its own
        // store. This is what makes the receiver's dashboard reachable at all.
        const linkFor = (dir) => {
          const link = execFileSync(exe, ['dashboard', '--store-dir', dir, '--print'], { cwd: WORK, encoding: 'utf8' });
          return (String(link).match(/tantu_bootstrap=([a-f0-9]+)/) || [])[1];
        };
        const tokA2 = linkFor(storeA2), tokB = linkFor(storeB);
        check('two-Hub: both Hubs mint an authenticated dashboard link',
          !!(tokA2 && tokB), 'a2=' + !!tokA2 + ' b=' + !!tokB);

        if (tokA2 && tokB) {
          const pageA2 = await context.newPage();
          const pageB = await context.newPage();
          await pageA2.goto('http://127.0.0.1:' + WEB_PORT_A2 + '/#tantu_bootstrap=' + tokA2);
          await pageB.goto('http://127.0.0.1:' + WEB_PORT_B + '/#tantu_bootstrap=' + tokB);
          await pageA2.waitForLoadState('networkidle').catch(() => {});
          await pageB.waitForLoadState('networkidle').catch(() => {});

          // A must see B as its destination. Not #dropPeerPills: those render
          // only when there is a choice to make, and with one peer the
          // destination is simply the default, so an empty pill list is correct
          // and asserting on it produced a failure against a working product.
          const sawPeer = await pageA2.evaluate(`(async function(){
            for (var i = 0; i < 60; i++) {
              var d = document.getElementById('destinationSummary');
              var l = document.getElementById('peerLabel');
              var dt = d ? d.textContent.replace(/\\s+/g,' ').trim() : '';
              var lt = l ? l.textContent.replace(/\\s+/g,' ').trim() : '';
              if (/Trusted/.test(lt)) return { destination: dt, peer: lt };
              await new Promise(function(r){ setTimeout(r, 250); });
            }
            return { destination: '', peer: '' };
          })()`);
          check('two-Hub: the sender resolves the real peer as its trusted destination',
            sawPeer && /Trusted/.test(sawPeer.peer), JSON.stringify(sawPeer));

          // Send text through the UI, exactly as a user would.
          const sentText = await pageA2.evaluate(`(async function(){
            var ta = document.getElementById('textPayload');
            ta.value = 'two-hub browser harness: ' + Date.now();
            ta.dispatchEvent(new Event('input', {bubbles:true}));
            await new Promise(function(r){ setTimeout(r, 200); });
            document.getElementById('btnSendText').click();
            for (var i = 0; i < 40; i++) {
              var ds = document.getElementById('dropStatus');
              if (ds && /Text sent|Not sent/.test(ds.textContent)) {
                return { sent: /Text sent/.test(ds.textContent), status: ds.textContent.trim() };
              }
              await new Promise(function(r){ setTimeout(r, 250); });
            }
            return { sent: false, why: 'no terminal status' };
          })()`);
          check('two-Hub: text sent from the browser reaches the real peer',
            sentText && sentText.sent === true, JSON.stringify(sentText));
          check('two-Hub: the transfer is reported as verified against the real peer',
            !!(sentText && /verified/.test(sentText.status || '')),
            JSON.stringify(sentText));

          // And it must arrive on B, shown by B's own dashboard.
          const received = await pageB.evaluate(`(async function(){
            var marker = 'two-hub browser harness';
            for (var i = 0; i < 80; i++) {
              var el = document.getElementById('receivedDropsList');
              if (el && el.textContent.indexOf(marker) >= 0) {
                return { found: true, text: el.textContent.replace(/\\s+/g,' ').trim().slice(0, 140) };
              }
              await new Promise(function(r){ setTimeout(r, 250); });
            }
            return { found: false, html: (document.getElementById('receivedDropsList')||{}).innerHTML ?
              (document.getElementById('receivedDropsList').innerHTML || '').slice(0, 140) : '(no list)' };
          })()`);
          check('two-Hub: the receiver dashboard shows the text that was sent',
            received && received.found === true, JSON.stringify(received));

          // Reachability, asserted from both directions in the same run.
          //
          // A false "Reachable" is the dangerous failure here: it would tell a
          // user their peer is there when it is not, which is worse than saying
          // nothing at all. The seeded TEST-NET peers below are the case for it --
          // 192.0.2.0/24 is unroutable by design, so no probe can ever succeed
          // against them and the chip must never claim otherwise.
          const readA2 = () => pageA2.evaluate(`(function () {
            var list = document.getElementById('peersList');
            var chips = document.querySelectorAll('#peersList [data-reach]');
            var t = [];
            for (var i = 0; i < chips.length; i++) {
              t.push((chips[i].getAttribute('data-reach') || '?') + ': ' +
                     (chips[i].innerText || '').replace(/\\s+/g, ' ').trim().slice(0, 60));
            }
            return {
              chips: t,
              rows: list ? list.children.length : -1,
              hasPeersList: !!list,
              rowsHtml: list ? list.innerHTML.slice(0, 60) : '',
            };
          })()`).catch((e) => ({ error: String(e) }));

          // Every reachability state, rendered by the product's own renderer.
//
// The sustained-failure copy cannot be reached end to end: the backoff starts at
// 60s and doubles, so a peer has to be genuinely absent for about three minutes
// before the wording changes, and no browser run waits that long to look at a
// chip. So the states are driven through renderPeersList instead, with the health
// the Hub would have produced. That is the real renderer and the real DOM --
// only the clock is supplied rather than waited for.
//
// The claim being held is the copy's: a peer that is up says so, a peer that
// just went quiet is not yet alarming, a peer that has been quiet through several
// checks says so and says what has been done about it, and a peer that has never
// answered is distinguished from one that has stopped.
const RENDER_STATES = await js(`(function () {
  function ago(secs) { return new Date(Date.now() - secs * 1000).toISOString(); }
  var cases = [
    ['reachable', { reachable: true,  last_checked: ago(5),    last_ok: ago(5),       consecutive_failures: 0 }],
    ['just-went-quiet', { reachable: false, last_checked: ago(30),  last_ok: ago(20),    consecutive_failures: 1 }],
    ['sustained', { reachable: false, last_checked: ago(400), last_ok: ago(600),   consecutive_failures: 7 }],
    ['never-answered', { reachable: false, last_checked: ago(400), last_ok: '',    consecutive_failures: 7 }],
    ['unprobed', { reachable: false, last_checked: '',       last_ok: '',        consecutive_failures: 0 }],
  ];
  var out = {};
  for (var i = 0; i < cases.length; i++) {
    var name = cases[i][0], health = cases[i][1];
    var list = document.getElementById('peersList');
    var before = list ? list.innerHTML : '';
    renderPeersList([{
      name: 'gate-peer', fingerprint: 'ff', address: '192.0.2.1:9877',
      sas: 'ABCDEF', active: false, is_default: false, health: health,
    }]);
    var chip = document.querySelector('#peersList [data-reach]');
    out[name] = {
      rendered: chip ? (chip.getAttribute('data-reach') + ' | ' +
                        (chip.innerText || '').replace(/\\s+/g, ' ').trim()) : null,
    };
    if (list) list.innerHTML = before;
  }
  return JSON.stringify(out);
})()`).catch((e) => JSON.stringify({ error: String(e) }));

const reachStates = (() => { try { return JSON.parse(RENDER_STATES); } catch (_) { return null; } })();
const wantStates = {
  // reachable -> says so, and says when.
  reachable: (c) => c && c.rendered && c.rendered.indexOf('ok |') === 0 && /Reachable/.test(c.rendered),
  // A single failed check is not yet a story; it must not claim otherwise.
  'just-went-quiet': (c) => c && c.rendered && c.rendered.indexOf('down |') === 0 &&
    /Not answering/.test(c.rendered) && !/checked/.test(c.rendered),
  // Persisted: says so, and says what has been done about it.
  sustained: (c) => c && c.rendered && c.rendered.indexOf('down |') === 0 &&
    /Not answering/.test(c.rendered) && /checked \d+ times/.test(c.rendered),
  // Never answered is a different problem from stopped answering, and saying
  // "last answered just now" for a peer that never has would be a lie.
  'never-answered': (c) => c && c.rendered && /has not answered since pairing/.test(c.rendered) &&
    !/last answered/.test(c.rendered),
  // No probe yet: no verdict at all, and specifically not a red one.
  unprobed: (c) => c && c.rendered === null,
};
for (const [name, predicate] of Object.entries(wantStates)) {
  check('peers: reachability copy for a peer that is "' + name + '"',
    predicate(reachStates && reachStates[name]),
    JSON.stringify(reachStates && reachStates[name]));
}

// The API payload is part of the detail, not just the DOM. A check
          // that only reports "no chips" cannot distinguish "the heartbeat never
          // ran" from "the heartbeat ran and the dashboard did not render it",
          // and those are different bugs in different places.
          const apiPeers = async () => pageA2.evaluate(`fetch('/api/status')
            .then(function (r) { return r.json(); })
            .then(function (j) {
              var ps = (j && j.peers) || [];
              return ps.map(function (p) {
                return (p.name || '?') + ' health=' +
                  (p.health ? JSON.stringify(p.health) : 'ABSENT');
              }).join(' | ');
            })
          `).catch((e) => '(api: ' + e + ')');

          // Look at the Peers tab before asking about it. The status poll
          // repaints the peer rows, and a page sitting on another tab may not
          // have repainted since the first probe landed -- so the API says the
          // peer is reachable while the DOM still shows a page from before that.
          // A user opens the Peers tab; so does this.
          await pageA2.click('#tab-peers').catch(() => {});
          await sleep(700);
          // Force one refresh by hand before reading. If the chip appears, the
          // poll loop was not repainting; if it does not, the chip itself is not
          // rendering. One forced call separates two bugs that look identical
          // from here.
          await pageA2.evaluate('typeof updateStatus === "function" ? updateStatus() : null')
            .catch(() => {});
          await sleep(900);

          let reachLive = await readA2();
          const reachLiveDeadline = Date.now() + 30000;
          while (Date.now() < reachLiveDeadline && !(reachLive && reachLive.chips.length > 0)) {
            await sleep(500);
            reachLive = await readA2();
          }
          check('two-Hub: a live paired peer is reported reachable',
            reachLive && Array.isArray(reachLive.chips) &&
            reachLive.chips.some((c) => c.indexOf('ok: ') === 0),
            // API first, and deliberately short. `check` truncates its detail at
            // 240 characters, so a verbose DOM dump pushes the one field that
            // distinguishes "the heartbeat never ran" from "the dashboard did not
            // render it" out of sight -- which is exactly what happened while
            // this was being written.
            JSON.stringify({ api: await apiPeers(), chips: reachLive ? reachLive.chips : null }));

          // The file leg, which is the one that proves bytes moved: a file has to
          // appear in B's own output directory with its contents intact.
          const twoHubFile = path.join(WORK, 'two-hub-proof.bin');
          fs.writeFileSync(twoHubFile, Buffer.alloc(64 * 1024, 0x5a));
          await pageA2.setInputFiles('#fileInput', twoHubFile);
          await sleep(700);
          await pageA2.evaluate('document.getElementById("btnConfirmSend").click(); "ok"').catch(() => {});
          let fileLanded = null;
          const byFile = Date.now() + 25000;
          while (Date.now() < byFile) {
            const got = fs.readdirSync(outB).filter((f) => /two-hub-proof/.test(f));
            if (got.length) {
              const bytes = fs.readFileSync(path.join(outB, got[0]));
              fileLanded = { name: got[0], size: bytes.length, allSame: bytes.every((b) => b === 0x5a) };
              break;
            }
            await sleep(400);
          }
          check('two-Hub: a file sent from the browser lands in the real receiver directory',
            !!fileLanded, JSON.stringify(fileLanded || { dir: outB, contents: fs.readdirSync(outB) }));
          check('two-Hub: the received file is byte-for-byte intact',
            !!(fileLanded && fileLanded.size === 64 * 1024 && fileLanded.allSame),
            JSON.stringify(fileLanded));
        }
        hubs2 = { a2, hb, outB };
      }
    } else {
      skip('two-Hub: both Hubs answer on their own ports', 'run with --two-hub');
    }

    // The cancel-acknowledgement budget ("under 1s"), measured for real.
    //
    // Row 2.5 recorded both transfer budgets as ungated because they "need a
    // live peer which the peerless harness cannot provide". Cancel turns out not
    // to need one, and the seeded blackhole peer is what makes it measurable: a
    // dial to TEST-NET-1 hangs instead of being refused, so there is a real
    // window in which a user could press Cancel. Against a refused dial there is
    // no window at all and the budget would be untestable rather than merely
    // untested.
    //
    // This is the genuine user experience being measured: press send, the
    // machine appears to hang, press Cancel, and the UI has to say so promptly.
    await tab('tab-drop');
    const picked = await js(`(function(){
      var pills = [].slice.call(document.querySelectorAll('#dropPeerPills button'));
      // The pill renders the peer's DisplayName, which is Alias when set. Matching the\n      // name would have passed against an empty list for the wrong reason.\n      var target = pills.filter(function(b){ return /Unreachable \\(blackhole\\)/.test(b.textContent); })[0];
      if (!target) return { ok:false, count:pills.length, texts:pills.map(function(b){return b.textContent;}) };
      target.click();
      return { ok:true, value:target.getAttribute('data-value') };
    })()`);
    // Only meaningful with a seeded store. Peerless, the Hub has no peers at all
    // and the upload goes to loopback instead -- which still exercises the cancel
    // path, so the budget is measured either way.
    if (WITH_PEERS) {
      check('transfer budget: the blackhole peer is selectable as a destination',
        picked && picked.ok === true, JSON.stringify(picked));
    } else {
      skip('transfer budget: the blackhole peer is selectable as a destination',
        'peerless run: no peer store is seeded, so the upload goes to loopback');
    }

    // Staged through the file picker, not a synthetic paste. Two reasons, both
    // found by running this on all three engines:
    //
    //  Firefox drops clipboardData on a synthetic ClipboardEvent, so the paste
    //  path stages nothing there and the budget silently did not run.
    //  A staged image left in the DOM tripped an unrelated existing check in
    //  webkit ("reduced motion kills all animation and transition", which found
    //  an IMG). A new check must not perturb the ones already there.
    //
    // The picker is also the primary path a user actually takes, so it is the
    // better thing to measure.
    const budgetFile = path.join(WORK, 'cancel-budget.bin');
    fs.writeFileSync(budgetFile, Buffer.alloc(3 * 1024 * 1024, 7));
    await page.setInputFiles('#fileInput', budgetFile);
    await sleep(900);
    const stagedForSend = await js(`(function(){
      var card = document.getElementById('filePreviewCard');
      var confirm = document.getElementById('btnConfirmSend');
      return {
        visible: card ? !card.hasAttribute('hidden') : false,
        canConfirm: confirm ? !confirm.disabled : false,
        dest: (document.getElementById('destinationSummary')||{}).textContent || ''
      };
    })()`);
    check('transfer budget: the file is staged for the chosen destination',
      stagedForSend && stagedForSend.visible === true, JSON.stringify(stagedForSend));

    let cancelMs = null;
    if (stagedForSend && stagedForSend.visible && stagedForSend.canConfirm) {
      await page.evaluate(`(function(){ document.getElementById('btnConfirmSend').click(); })()`);
      // Wait for the in-flight state that offers a Cancel.
      let inFlight = false;
      for (let i = 0; i < 40 && !inFlight; i++) {
        inFlight = await js(`(function(){
          var b = document.getElementById('btnCancelUpload');
          if (!b) return false;
          var s = getComputedStyle(b);
          return s.display !== 'none';
        })()`);
        if (!inFlight) await sleep(150);
      }
      check('transfer budget: an in-flight upload offers a Cancel control', inFlight === true, '');

      if (inFlight) {
        const t0 = Date.now();
        await page.evaluate(`(function(){ document.getElementById('btnCancelUpload').click(); })()`);
        let acked = false;
        let text = '';
        for (let i = 0; i < 40 && !acked; i++) {
          const r = await js(`(function(){
            var bar = document.getElementById('uploadStatus');
            var txt = bar ? bar.textContent : '';
            var b = document.getElementById('btnCancelUpload');
            var hidden = !b || getComputedStyle(b).display === 'none';
            return { txt: txt, hidden: hidden };
          })()`);
          text = r ? r.txt : '';
          if (/cancel/i.test(text) || (r && r.hidden && i > 2)) { acked = true; break; }
          await sleep(50);
        }
        cancelMs = Date.now() - t0;
        // Surfaced rather than only stored in the JSON report, which lives in a temp
        // directory this process deletes on exit. Same reasoning as the first-render
        // timing already printed by this harness.
        console.log('uxtest: cancel acknowledged in ' + cancelMs + 'ms (budget 1000ms)');
        check('transfer budget: cancelling is acknowledged to the user',
          acked === true, 'after ' + cancelMs + 'ms, status text: ' + String(text).slice(0, 80));
        // Leave nothing behind. This block stages a file and runs an upload, and
        // a staged preview left visible made an unrelated existing check fail:
        // webkit's "reduced motion kills all transition" began reporting an IMG,
        // because a thumbnail from the earlier clipboard check was still attached
        // to the preview card this one reopened. Confirmed by removing this block
        // and watching webkit go back to 75/75.
        //
        // A new check must not perturb the ones already there.
        await js(`(function(){
          var b = document.querySelector('[data-action="cancel-preview"]');
          if (b) b.click();
          return 'dismissed';
        })()`);
        await sleep(300);
        check('transfer budget: the staged preview is dismissed afterwards',
          (await js(`(function(){
            var card = document.getElementById('filePreviewCard');
            return card ? card.hasAttribute('hidden') : false;
          })()`)) === true, '');

        check('transfer budget: cancel acknowledgement is under 1s',
          cancelMs !== null && cancelMs < 1000, 'took ' + cancelMs + 'ms');
      }
    } else {
      skip('transfer budget: cancel acknowledgement',
        'the file could not be staged for send, so the upload could not be started');
    }
    fs.writeFileSync(path.join(out, 'uxtest-report.json'), JSON.stringify({ head, engine: ENGINE, peers: WITH_PEERS, checks, skips }, null, 1));
    const pass = checks.filter((c) => c.ok).length;
    if (skips.length) console.log('uxtest: ' + skips.length + ' check(s) SKIPPED -- see the SKIP lines above');
    console.log('\nuxtest: ' + pass + '/' + checks.length + ' checks passed (' + ENGINE + '; screenshots in ' + out + ')');
    // A run that went red only because the Hub vanished is not a product
    // result, and reporting it as 47/51 checks passed would be a false claim
    // about the product's behaviour.
    //
    // Exit 4 is assigned in the same statement as the pass/fail code, because an
    // earlier version set it in a block *above* this line and the plain
    // pass/fail assignment then overwrote it -- verified only by reading the
    // exit code back, since the summary text looked correct either way.
    const productFailed = pass !== checks.length;
    const infraFailed = hubDeath !== null && productFailed;
    if (infraFailed) {
      console.error('\nuxtest: NOT A PRODUCT RESULT - ' + hubDeath + '\n'
        + 'The Hub under test exited mid-run, so the remaining checks had no\n'
        + 'server to talk to. Re-run on a quiet machine. Last Hub output:\n'
        + (lastHubOut.trim() || '(none)'));
    }
    process.exitCode = infraFailed ? 4 : (productFailed ? 1 : 0);
  } catch (e) {
    console.error('uxtest ERROR: ' + e.message);
    process.exitCode = 3;
  } finally {
    if (browser) { try { await browser.close(); } catch (_) {} }
      if (hubs2) {
        for (const p of [hubs2.a2, hubs2.hb]) { if (p) { try { p.kill(); } catch (e) {} } }
      }
    if (hub) { try { hub.kill(); } catch (_) {} }
    // The browser and its profile can keep file handles for a moment after
    // exit, so a single delete can fail. Retry, and say so rather than
    // leaving a directory behind silently.
    for (let attempt = 0; attempt < 5; attempt++) {
      try { fs.rmSync(WORK, { recursive: true, force: true }); break; }
      catch (_) { await sleep(600); }
    }
    if (fs.existsSync(WORK)) {
      console.log('uxtest: could not remove ' + WORK + ' (Windows file locks). Remove it at your convenience.');
    }
  }
}

await main();

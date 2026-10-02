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
// Requires: Node 18+, `npm ci` in this directory, and the browser builds from
// `npx playwright install chromium firefox webkit`. None of that is needed to
// build, test, or run Tantu itself.

import { spawn, execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, firefox, webkit } from 'playwright';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');
const WORK = path.join(os.tmpdir(), 'tantu-uxtest-' + process.pid);
const WEB_PORT = 18976;
const P2P_PORT = 19877;
const WITH_PEERS = process.argv.includes('--peers');
const ENGINES = { chromium, firefox, webkit };
const ENGINE = (() => {
  const arg = process.argv.find((a) => a.startsWith('--browser='));
  const v = (arg ? arg.split('=')[1] : process.env.TANTU_TEST_BROWSER) || 'chromium';
  return String(v).toLowerCase();
})();

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const checks = [];
function check(name, ok, detail) {
  checks.push({ name, ok: !!ok, detail: String(detail ?? '').slice(0, 240) });
  if (!ok) console.log('  FAIL  ' + name + '  ' + String(detail ?? '').slice(0, 200));
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
  const out = path.join(WORK, 'out');
  const exe = path.join(WORK, 'tantu' + (process.platform === 'win32' ? '.exe' : ''));
  fs.mkdirSync(store, { recursive: true }); fs.mkdirSync(out, { recursive: true });
  if (WITH_PEERS) seedPeers(store);

  let hub = null, browser = null, context = null, page = null;
  try {
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
    // "fetch failed" — indistinguishable from a slow machine, a crashed Hub,
    // or another Tantu already holding one of these ports. Probe until it
    // answers, and if it never does, say exactly what happened.
    let hubOut = '';
    hub.stdout.on('data', (d) => { hubOut += d; });
    hub.stderr.on('data', (d) => { hubOut += d; });

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
        + '\nAnother Tantu holding ' + WEB_PORT + ' or ' + P2P_PORT + ' is the usual cause — this harness needs both ports free.');
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

    // Console hygiene feed: uncaught exceptions and console.error output,
    // replacing CDP's Runtime.exceptionThrown and Log.entryAdded.
    const consoleErrors = [];
    // Set while the staleness check deliberately cuts /api/status: the browser
    // logs each blocked poll as a resource error. That is the harness's own
    // interference, not dashboard noise — CDP's setBlockedURLs produced no
    // Log.entryAdded for it either, so excluding it keeps the check's meaning
    // (unexpected console errors) rather than its mechanism.
    let statusBlocked = false;
    // The stack's first frames are kept: a message like WebKit's
    // "… due to access control checks" names no caller, and the caller is the
    // entire question when an engine disagrees with the other two.
    page.on('pageerror', (e) => consoleErrors.push('pageerror: ' + e.message
      + (e.stack ? ' @ ' + String(e.stack).split('\n').slice(0, 3).join(' | ') : '')));
    page.on('console', (m) => {
      if (m.type() !== 'error') return;
      const loc = (typeof m.location === 'function' ? m.location() : null) || {};
      const entry = 'console: ' + m.text() + (loc.url ? ' @ ' + loc.url : '');
      if (statusBlocked && /\/api\/status/.test(entry)) return;
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
      // display check above and still generate no boxes — its ancestor (an
      // inactive tab pane) is display:none — and then there is nothing on
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

    // Navigate, then wait for the bootstrap exchange to settle rather than
    // assuming four seconds is enough: the exchange POSTs, strips the fragment
    // and reloads, so "the fragment is gone" means the page is done booting.
    await page.goto(`http://127.0.0.1:${WEB_PORT}/#tantu_bootstrap=${token}`);
    let settled = false;
    for (let i = 0; i < 60 && !settled; i++) {
      const hash = await js('location.hash');
      settled = typeof hash === 'string' && hash === '';
      if (!settled) await sleep(250);
    }
    await sleep(500);

    // --- session and the send preview (the CSP regression) ---
    const sess = await js('(function(){return {banner:getComputedStyle(document.getElementById("sessionBanner")).display, dest:document.getElementById("destinationSummary").textContent, url:location.href};})()');
    check('session established', sess && sess.banner === 'none', JSON.stringify(sess));
    check('destination always visible', sess && /Destination: .+/.test(sess.dest || ''), sess && sess.dest);
    check('one-time token stripped from the URL', sess && !/tantu_bootstrap/.test(sess.url), sess && sess.url);

    await js(`(function(){var b='iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';
      var bin=atob(b), arr=new Uint8Array(bin.length); for(var i=0;i<bin.length;i++) arr[i]=bin.charCodeAt(i);
      stageFileForSend(new File([arr],'uxtest-shot.png',{type:'image/png'})); return 'ok';})()`);
    await sleep(1200);
    const thumb = await js('(function(){var t=document.getElementById("filePreviewThumb");return {nw:t.naturalWidth, loaded:t.classList.contains("loaded"), op:getComputedStyle(t).opacity, dest:document.getElementById("filePreviewDest").textContent};})()');
    check('preview thumbnail decodes (CSP allows blob:)', thumb && thumb.nw > 0, JSON.stringify(thumb));
    check('preview thumbnail is visible', thumb && thumb.loaded && Number(thumb.op) > 0.9, JSON.stringify(thumb));
    check('preview names its destination', thumb && /Destination: /.test(thumb.dest || ''), thumb && thumb.dest);
    await js('cancelPendingSend(); "ok"');

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

    // --- staleness ---
    await context.route('**/api/status*', (route) => route.abort('failed'));
    statusBlocked = true;
    await sleep(9000);
    const stale = await js('(function(){return {b:document.getElementById("staleBanner").style.display, t:document.getElementById("staleBanner").textContent.replace(/\\s+/g," ").trim(), a:document.getElementById("destinationSummary").getAttribute("data-stale"), dest:document.getElementById("destinationSummary").textContent.length>0};})()');
    check('unreachable Hub raises a staleness banner', stale && stale.b === 'block', JSON.stringify(stale));
    check('banner says last-known and offers a retry', stale && /last known/i.test(stale.t) && /retry/i.test(stale.t), stale && stale.t);
    check('destination is labelled, never hidden', stale && stale.a === 'true' && stale.dest, JSON.stringify(stale));
    await context.unroute('**/api/status*');
    await sleep(300);
    await js('(function(){var b=document.querySelector(\'[data-action="retry-status"]\');if(b)b.click();return "ok";})()');
    await sleep(2500);
    const live2 = await js('(function(){return {b:document.getElementById("staleBanner").style.display, a:document.getElementById("destinationSummary").getAttribute("data-stale")};})()');
    check('staleness clears on recovery', live2 && live2.b === 'none' && live2.a === null, JSON.stringify(live2));
    // Only now, with the Hub answering again, does a /api/status console
    // error mean something unexpected — so only now is one recorded.
    statusBlocked = false;

    // --- contrast, both themes ---
    const TARGETS = ['#nextActionBanner', '#downloadDirPath', '#idSAS', '#logConsole .log-entry', '#logConsole .log-entry span', '.version-tag', '.pill', '.pill.active', '.stat-val', '.hint-text', '.tag-drop', '.tag-error', '.drop-zone-subtext', '#destinationSummary', '#peerLabel', '.stale-banner'];
    // A failing contrast number is the only thing the loop records, so a
    // sweep that silently measured nothing would still report green: count
    // what it actually measured and assert that below.
    // CONTRAST_MIN_MEASURED is the floor: every rendered target across every
    // tab, in both themes (visibility must not depend on the theme). It is
    // also the canary for the machinery itself — if tab activation breaks,
    // whole panes go display:none and the count drops below it.
    const CONTRAST_MIN_MEASURED = 26;
    const measured = { dark: 0, light: 0 };
    for (const s of ['dark', 'light']) {
      await scheme(s); await sleep(200);
      for (const t of ['tab-drop', 'tab-relay', 'tab-peers', 'tab-transfers', 'tab-logs']) {
        await tab(t);
        for (const sel of TARGETS) {
          const v = await CONTRAST(sel);
          if (v) measured[s]++;
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
    // skip — including a regression in the rendered-only rule above.
    check('contrast sweep saw the same rendered targets in both themes',
      measured.dark === measured.light, JSON.stringify(measured));
    check('contrast sweep measured a full complement of rendered targets',
      measured.light >= CONTRAST_MIN_MEASURED,
      JSON.stringify(measured) + ' (floor ' + CONTRAST_MIN_MEASURED + ')');
    console.log('uxtest: contrast measured ' + JSON.stringify(measured));
    await scheme('dark');

    // --- 200% zoom, narrow viewport, reduced motion ---
    await tab('tab-drop');
    await metrics(640, 450); await sleep(400);
    const zoom = await js('(function(){var d=document.documentElement;return {h:d.scrollWidth>d.clientWidth};})()');
    check('200% zoom: no horizontal scrolling', !zoom.h, JSON.stringify(zoom));
    await shot('zoom200');
    await metrics(360, 780); await sleep(400);
    for (const [t, label] of [['tab-drop', 'QuickDrop'], ['tab-relay', 'Relay'], ['tab-peers', 'Peers'], ['tab-transfers', 'Transfers'], ['tab-logs', 'Logs']]) {
      await tab(t);
      const n = await js('(function(){var d=document.documentElement;return {w:d.scrollWidth,c:d.clientWidth};})()');
      check('360px ' + label + ': no page overflow', n.w <= n.c, JSON.stringify(n));
    }
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
    // because it never clicked. That is not hypothetical — "Cancel sign-in"
    // read its operation id from `this`, which in a listener bound to
    // `document` is `document`, and threw "getAttribute is not a function" on
    // every click while 29/29 checks passed.
    //
    // So sweep every data-action the page can render — including the ones only
    // live data produces — and fail on any that throws. Dialogs, the OS shell,
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

      keep(window, 'alert', function () {});
      keep(window, 'prompt', function () { return null; });   // "cancelled": the handler stops before mutating
      keep(window, 'confirm', function () { return false; });  // same
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
      // would never submit. Every other call — including the cancel this
      // sweep exists to prove — reaches the real Hub.
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
        // An exception thrown inside a listener does not reach this call —
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
    await sleep(300);

    // --- console hygiene ---
    // Three classes are harness/browser noise, each narrowly scoped:
    // favicon, the javascript: URL the sanitization test deliberately feeds,
    // and 502/503/504 from the intentional oversize upload. The last one is
    // WebKit-specific and documented rather than guessed at: WebKit cancels
    // any fetch issued while a navigation is pending — the dashboard's
    // bootstrap reloads the document — and mislabels the cancellation as
    // "… due to access control checks", a CORS phrasing for a request that
    // was never a CORS request (confirmed via a WebKit debug build:
    // supabase/supabase#20982 — "canceled because there was a pending
    // navigation … logged with this 'access control' error"; also SO
    // 63141448, "after reload"). The product's only /api/pair/pending call
    // site is try/caught in full (loadPendingPairings), a real same-origin
    // CORS defect would still be reported by chromium and firefox — whose
    // CORS messages are not filtered — and the check stays a pageerror-only
    // exception, so any other uncaught exception fails regardless of engine.
    const noise = (x) => /favicon\.ico/.test(x) || /javascript%3Aalert/.test(x)
      || (/api\/drop\/upload/.test(x) && /50[023]/.test(x))
      || (/^pageerror: /.test(x) && / due to access control checks\.$/.test(x));
    const errs = consoleErrors.filter((x) => !noise(x));
    check('zero unexpected console errors', errs.length === 0, JSON.stringify(errs.slice(0, 2)));

    fs.writeFileSync(path.join(out, 'uxtest-report.json'), JSON.stringify({ head, engine: ENGINE, peers: WITH_PEERS, checks }, null, 1));
    const pass = checks.filter((c) => c.ok).length;
    console.log('\nuxtest: ' + pass + '/' + checks.length + ' checks passed (' + ENGINE + '; screenshots in ' + out + ')');
    process.exitCode = pass === checks.length ? 0 : 1;
  } catch (e) {
    console.error('uxtest ERROR: ' + e.message);
    process.exitCode = 3;
  } finally {
    if (browser) { try { await browser.close(); } catch (_) {} }
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

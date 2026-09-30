// Dashboard acceptance harness: real headless Chrome over the DevTools
// Protocol, with no dependencies beyond Node's own globals.
//
// It builds and starts an isolated Hub, seeds a peer store, drives the real
// dashboard, and asserts on measured runtime behaviour: computed contrast in
// both OS themes, real keyboard focus order and visibility, 200% zoom, a narrow
// viewport, reduced motion, and the console error log.
//
// A marker test cannot catch a blocked image, a destroyed focus ring, or a
// live region that announces twice a second. That is why this exists.
//
// Usage: node uxtest/run.mjs [--peers]
//   --peers   seed three trusted peers, exercising the multi-peer layout
//
// Requires: Node 18+ and a Chrome or Edge binary. Neither is needed to build,
// test, or run Tantu itself.

import { spawn, execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');
const WORK = path.join(os.tmpdir(), 'tantu-uxtest-' + process.pid);
const WEB_PORT = 18976;
const P2P_PORT = 19877;
const DEBUG_PORT = 19470;
const WITH_PEERS = process.argv.includes('--peers');

const CHROME_CANDIDATES = [
  process.env.TANTU_TEST_CHROME,
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser',
].filter(Boolean);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const checks = [];
function check(name, ok, detail) {
  checks.push({ name, ok: !!ok, detail: String(detail ?? '').slice(0, 240) });
  if (!ok) console.log('  FAIL  ' + name + '  ' + String(detail ?? '').slice(0, 200));
}
function findChrome() {
  for (const c of CHROME_CANDIDATES) {
    const hit = firstExisting(c);
    if (hit) return hit;
  }
  return null;
}
// TANTU_TEST_CHROME may be a pattern: CI points it at whatever version
// @puppeteer/browsers just unpacked (/tmp/browsers/chrome/linux-*/...), and
// the install directory is not known until the install step has run. One '*'
// in a single path segment is supported, which is the shape installers
// produce; a literal path is checked as-is, so a typo still reads as a
// missing binary rather than a bad glob.
function firstExisting(candidate) {
  if (!candidate) return null;
  if (!candidate.includes('*')) return fs.existsSync(candidate) ? candidate : null;
  const star = candidate.indexOf('*');
  const cut = Math.max(candidate.lastIndexOf('/', star), candidate.lastIndexOf('\\', star)) + 1;
  const dir = candidate.slice(0, cut);
  const tail = candidate.slice(cut);
  const sep = tail.search(/[\\/]/);
  const partial = tail.slice(0, sep === -1 ? tail.length : sep).replace('*', '');
  const rest = sep === -1 ? null : tail.slice(sep + 1);
  let entries;
  try { entries = fs.readdirSync(dir); } catch { return null; }
  for (const e of entries) {
    if (!e.startsWith(partial)) continue;
    const full = rest === null ? path.join(dir, e) : path.join(dir, e, rest);
    if (fs.existsSync(full)) return full;
  }
  return null;
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

// ------------------------------------------------------------------- CDP glue
class CDP {
  constructor(url) {
    this.ws = new WebSocket(url); this.id = 0; this.pending = new Map(); this.events = [];
    this.ready = new Promise((res, rej) => { this.ws.onopen = res; this.ws.onerror = rej; });
    this.ws.onmessage = (e) => {
      const m = JSON.parse(e.data);
      if (m.id && this.pending.has(m.id)) {
        const p = this.pending.get(m.id); this.pending.delete(m.id);
        m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result);
      } else if (m.method) this.events.push(m);
    };
  }
  send(method, params = {}) {
    const id = ++this.id;
    return new Promise((res, rej) => {
      this.pending.set(id, { resolve: res, reject: rej });
      this.ws.send(JSON.stringify({ id, method, params }));
      setTimeout(() => { if (this.pending.has(id)) { this.pending.delete(id); rej(new Error('timeout ' + method)); } }, 60000);
    });
  }
  async js(expr) {
    const r = await this.send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) return null;
    return r.result.value;
  }
}

// ------------------------------------------------------------------ the suite
async function main() {
  const chromePath = findChrome();
  if (!chromePath) {
    console.error('No Chrome or Edge binary found. Set TANTU_TEST_CHROME to run this harness.');
    process.exitCode = 2; return;
  }
  const store = path.join(WORK, 'store');
  const out = path.join(WORK, 'out');
  const profile = path.join(WORK, 'profile');
  const exe = path.join(WORK, 'tantu' + (process.platform === 'win32' ? '.exe' : ''));
  fs.mkdirSync(store, { recursive: true }); fs.mkdirSync(out, { recursive: true }); fs.mkdirSync(profile, { recursive: true });
  if (WITH_PEERS) seedPeers(store);

  let hub = null, browser = null;
  try {
    console.log('uxtest: building');
    go(['build', '-o', exe, './cmd/tantu']);
    const head = git(['rev-parse', '--short', 'HEAD']).trim();
    console.log('uxtest: HEAD ' + head + (WITH_PEERS ? ' (3 seeded peers)' : ' (peerless)'));

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

    browser = spawn(chromePath, ['--headless=new', '--disable-gpu', '--no-first-run',
      '--no-default-browser-check', '--hide-scrollbars', '--user-data-dir=' + profile,
      '--remote-debugging-port=' + DEBUG_PORT, 'about:blank'], { stdio: 'ignore' });
    let target = null;
    for (let i = 0; i < 150 && !target; i++) {
      try { const l = await (await fetch('http://127.0.0.1:' + DEBUG_PORT + '/json/list')).json(); target = l.find((t) => t.type === 'page'); } catch (_) {}
      if (!target) await sleep(200);
    }
    if (!target) throw new Error('no page target');
    const cdp = new CDP(target.webSocketDebuggerUrl);
    await cdp.ready;
    await cdp.send('Runtime.enable'); await cdp.send('Page.enable'); await cdp.send('Log.enable');
    await cdp.send('Network.enable');
    // Headless Chrome never focuses the document, so :focus never matches and
    // focus-visibility cannot be measured without this.
    await cdp.send('Emulation.setFocusEmulationEnabled', { enabled: true });

    const scheme = (s) => cdp.send('Emulation.setEmulatedMedia', { media: 'screen', features: [{ name: 'prefers-color-scheme', value: s }] });
    const metrics = (w, h, sc) => cdp.send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: sc || 1, mobile: false });
    const tab = async (id) => { await cdp.js(`document.querySelector('[data-tab="${id}"]').click(); 'ok'`); await sleep(300); };
    const shot = async (n) => { const r = await cdp.send('Page.captureScreenshot', { format: 'png' }); if (r && r.data) fs.writeFileSync(path.join(out, n + '.png'), Buffer.from(r.data, 'base64')); };

    const CONTRAST = (sel) => cdp.js(`(function(sel){
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
      var bg=effBg(el), l1=lum(hex(s.color)), l2=lum(bg);
      var size=parseFloat(s.fontSize), bold=parseInt(s.fontWeight,10)>=700;
      var need=(size>=24||(size>=18.66&&bold))?3:4.5;
      return {ratio:+(((Math.max(l1,l2)+0.05)/(Math.min(l1,l2)+0.05)).toFixed(2)), need:need, size:size};})(${JSON.stringify(sel)})`);

    // Navigate, then wait for the bootstrap exchange to settle rather than
    // assuming four seconds is enough: the exchange POSTs, strips the fragment
    // and reloads, so "the fragment is gone" means the page is done booting.
    await cdp.send('Page.navigate', { url: `http://127.0.0.1:${WEB_PORT}/#tantu_bootstrap=${token}` });
    let settled = false;
    for (let i = 0; i < 60 && !settled; i++) {
      const hash = await cdp.js('location.hash');
      settled = typeof hash === 'string' && hash === '';
      if (!settled) await sleep(250);
    }
    await sleep(500);

    // --- session and the send preview (the CSP regression) ---
    const sess = await cdp.js('(function(){return {banner:getComputedStyle(document.getElementById("sessionBanner")).display, dest:document.getElementById("destinationSummary").textContent, url:location.href};})()');
    check('session established', sess && sess.banner === 'none', JSON.stringify(sess));
    check('destination always visible', sess && /Destination: .+/.test(sess.dest || ''), sess && sess.dest);
    check('one-time token stripped from the URL', sess && !/tantu_bootstrap/.test(sess.url), sess && sess.url);

    await cdp.js(`(function(){var b='iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';
      var bin=atob(b), arr=new Uint8Array(bin.length); for(var i=0;i<bin.length;i++) arr[i]=bin.charCodeAt(i);
      stageFileForSend(new File([arr],'uxtest-shot.png',{type:'image/png'})); return 'ok';})()`);
    await sleep(1200);
    const thumb = await cdp.js('(function(){var t=document.getElementById("filePreviewThumb");return {nw:t.naturalWidth, loaded:t.classList.contains("loaded"), op:getComputedStyle(t).opacity, dest:document.getElementById("filePreviewDest").textContent};})()');
    check('preview thumbnail decodes (CSP allows blob:)', thumb && thumb.nw > 0, JSON.stringify(thumb));
    check('preview thumbnail is visible', thumb && thumb.loaded && Number(thumb.op) > 0.9, JSON.stringify(thumb));
    check('preview names its destination', thumb && /Destination: /.test(thumb.dest || ''), thumb && thumb.dest);
    await cdp.js('cancelPendingSend(); "ok"');

    // --- text composer reports its outcome inline ---
    await cdp.js('(function(){document.getElementById("textPayload").value="uxtest";document.querySelector("[data-action=\'send-text\']").click();return "ok";})()');
    await sleep(2500);
    const textRes = await cdp.js('(function(){var s=document.getElementById("dropStatus");return {d:getComputedStyle(s).display,t:s.textContent,c:s.className};})()');
    const okOrHonest = textRes && textRes.d !== 'none' && (/sent to/i.test(textRes.t) || /not sent|could not reach/i.test(textRes.t));
    check('text send reports an outcome on this tab', okOrHonest, JSON.stringify(textRes));
    await cdp.js('(function(){document.getElementById("textPayload").value="q".repeat(11*1024*1024);document.querySelector("[data-action=\'send-text\']").click();return "ok";})()');
    await sleep(1500);
    const over = await cdp.js('(function(){var s=document.getElementById("dropStatus");return {d:getComputedStyle(s).display,t:s.textContent,kept:document.getElementById("textPayload").value.length>1000000};})()');
    check('oversize text warns inline and keeps the draft', over && over.d !== 'none' && /too large|limit/i.test(over.t) && over.kept, JSON.stringify(over));
    await cdp.js('document.getElementById("textPayload").value=""; "ok"');

    // --- structure: headings, landmarks, bypass, log console ---
    await tab('tab-logs');
    const struct = await cdp.js(`(function(){
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
    const pairing = await cdp.js(`(function(){
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
    await cdp.send('Network.setBlockedURLs', { urls: ['*/api/status*'] });
    await sleep(9000);
    const stale = await cdp.js('(function(){return {b:document.getElementById("staleBanner").style.display, t:document.getElementById("staleBanner").textContent.replace(/\\s+/g," ").trim(), a:document.getElementById("destinationSummary").getAttribute("data-stale"), dest:document.getElementById("destinationSummary").textContent.length>0};})()');
    check('unreachable Hub raises a staleness banner', stale && stale.b === 'block', JSON.stringify(stale));
    check('banner says last-known and offers a retry', stale && /last known/i.test(stale.t) && /retry/i.test(stale.t), stale && stale.t);
    check('destination is labelled, never hidden', stale && stale.a === 'true' && stale.dest, JSON.stringify(stale));
    await cdp.send('Network.setBlockedURLs', { urls: [] });
    await sleep(300);
    await cdp.js('(function(){var b=document.querySelector(\'[data-action="retry-status"]\');if(b)b.click();return "ok";})()');
    await sleep(2500);
    const live2 = await cdp.js('(function(){return {b:document.getElementById("staleBanner").style.display, a:document.getElementById("destinationSummary").getAttribute("data-stale")};})()');
    check('staleness clears on recovery', live2 && live2.b === 'none' && live2.a === null, JSON.stringify(live2));

    // --- contrast, both themes ---
    const TARGETS = ['#nextActionBanner', '#downloadDirPath', '#idSAS', '#logConsole .log-entry', '#logConsole .log-entry span', '.version-tag', '.pill', '.pill.active', '.stat-val', '.hint-text', '.tag-drop', '.tag-error', '.drop-zone-subtext', '#destinationSummary', '#peerLabel', '.stale-banner'];
    for (const s of ['dark', 'light']) {
      await scheme(s); await sleep(200);
      for (const t of ['tab-drop', 'tab-relay', 'tab-peers', 'tab-transfers', 'tab-logs']) {
        await tab(t);
        for (const sel of TARGETS) {
          const v = await CONTRAST(sel);
          if (v && v.ratio < v.need) check('contrast ' + s + ' ' + sel, false, JSON.stringify(v));
        }
      }
    }
    await scheme('dark');

    // --- 200% zoom, narrow viewport, reduced motion ---
    await tab('tab-drop');
    await metrics(640, 450, 2); await sleep(400);
    const zoom = await cdp.js('(function(){var d=document.documentElement;return {h:d.scrollWidth>d.clientWidth};})()');
    check('200% zoom: no horizontal scrolling', !zoom.h, JSON.stringify(zoom));
    await shot('zoom200');
    await metrics(360, 780, 1); await sleep(400);
    for (const [t, label] of [['tab-drop', 'QuickDrop'], ['tab-relay', 'Relay'], ['tab-peers', 'Peers'], ['tab-transfers', 'Transfers'], ['tab-logs', 'Logs']]) {
      await tab(t);
      const n = await cdp.js('(function(){var d=document.documentElement;return {w:d.scrollWidth,c:d.clientWidth};})()');
      check('360px ' + label + ': no page overflow', n.w <= n.c, JSON.stringify(n));
    }
    await shot('narrow360');
    await metrics(1280, 900, 1); await sleep(300);
    await cdp.send('Emulation.setEmulatedMedia', { media: 'screen', features: [{ name: 'prefers-color-scheme', value: 'dark' }, { name: 'prefers-reduced-motion', value: 'reduce' }] });
    await sleep(300);
    const motion = await cdp.js(`(function(){
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
    const sweep = await cdp.js(`(async function () {
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
    const noise = (x) => /favicon\.ico/.test(x) || /javascript%3Aalert/.test(x) || (/api\/drop\/upload/.test(x) && /50[023]/.test(x));
    const errs = cdp.events.filter((e) => e.method === 'Runtime.exceptionThrown' || (e.method === 'Log.entryAdded' && e.params?.entry?.level === 'error'))
      .map((e) => JSON.stringify(e.params)).filter((x) => !noise(x));
    check('zero unexpected console errors', errs.length === 0, JSON.stringify(errs.slice(0, 2)));

    fs.writeFileSync(path.join(out, 'uxtest-report.json'), JSON.stringify({ head, peers: WITH_PEERS, checks }, null, 1));
    const pass = checks.filter((c) => c.ok).length;
    console.log('\nuxtest: ' + pass + '/' + checks.length + ' checks passed (screenshots in ' + out + ')');
    process.exitCode = pass === checks.length ? 0 : 1;
  } catch (e) {
    console.error('uxtest ERROR: ' + e.message);
    process.exitCode = 3;
  } finally {
    if (browser) browser.kill();
    if (hub) { try { hub.kill(); } catch (_) {} }
    // Chrome keeps file handles on its profile for a moment after exit, so a
    // single delete can fail. Retry, and say so rather than leaving a
    // directory behind silently.
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

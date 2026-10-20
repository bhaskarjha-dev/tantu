// Populated-dashboard capture: starts a Hub with a seeded peer store, drives
// real traffic through the real UI (a text send, a file send, a relay
// attempt), and captures every tab.
//
// Not part of the acceptance suite. The suite proves the page is not broken;
// this exists so the page can be REVIEWED in the state a user actually meets
// it -- with peers, received items, transfer records and live log lines in
// it. An empty dashboard is mostly empty states, and empty states are the
// easiest thing in the world to make look good.
//
//   node tools/uxtest/populated.mjs <outDir> [dark|light]
//
import { chromium } from 'playwright';
import { spawn, execFileSync } from 'child_process';
import fs from 'fs';
import os from 'os';
import path from 'path';

const REPO = path.resolve(process.cwd());
const outDir = process.argv[2];
const scheme = process.env.POPULATE_SCHEME || process.argv[3] || 'dark';

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'tantu-pop-'));
const exe = path.join(tmp, 'tantu.exe');
execFileSync('go', ['build', '-o', exe, './cmd/tantu'], { cwd: REPO, stdio: 'inherit' });
const store = path.join(tmp, 'store');
fs.mkdirSync(store, { recursive: true });

// The same four peers the acceptance suite seeds: a default, a long-name
// peer, a short one, and a blackhole.
const peers = {
  a1: { fingerprint: 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90', name: 'devbox', alias: 'Devbox (workstation)', is_default: true, address: '192.168.1.50:9877', cert_pem: null, first_seen: '2026-09-01T09:00:00Z', last_seen: '2026-09-26T23:40:00Z' },
  b1: { fingerprint: 'b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1', name: 'build-box-with-a-deliberately-long-hostname-that-should-truncate-cleanly', address: '10.0.12.7:9877', cert_pem: null, first_seen: '2026-08-14T11:20:00Z', last_seen: '2026-09-20T08:05:00Z' },
  c1: { fingerprint: 'c3', name: 'tiny-fp', address: 'not-a-host:0', cert_pem: null, first_seen: '2026-07-01T00:00:00Z', last_seen: '2026-07-01T00:00:00Z' },
  d1: { fingerprint: 'd4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3', name: 'unreachable-peer', alias: 'Unreachable (blackhole)', address: '192.0.2.1:9877', cert_pem: null, first_seen: '2026-10-07T00:00:00Z', last_seen: '2026-10-07T00:00:00Z' },
};
const byFp = {};
for (const p of Object.values(peers)) byFp[p.fingerprint] = p;
if (process.env.POPULATE_PEERS !== '0') {
  fs.writeFileSync(path.join(store, 'peers.json'), JSON.stringify(byFp, null, 2));
}

const hub = spawn(exe, ['hub', '--headless', '--transport', 'loopback', '--web-addr', '127.0.0.1:0', '--store-dir', store],
  { stdio: ['ignore', 'pipe', 'pipe'] });
let hubOut = '';
hub.stdout.on('data', (d) => { hubOut += d; });
hub.stderr.on('data', (d) => { hubOut += d; });

const port = await new Promise((res, rej) => {
  const t = setTimeout(() => rej(new Error('the Hub never reported a web port:\n' + hubOut)), 30000);
  const iv = setInterval(() => {
    const m = hubOut.match(/Web Dashboard:\s*http:\/\/127\.0\.0\.1:(\d+)/);
    if (m) { clearInterval(iv); clearTimeout(t); res(m[1]); }
  }, 150);
});

// A one-time link: the page needs a session to reach the APIs that carry
// the peers, the transfers and the log.
const link = execFileSync(exe, ['dashboard', '--store-dir', store, '--print'], { cwd: REPO, encoding: 'utf8' });
const linkURL = (String(link).match(/http:\/\/\S+/) || [])[0];
if (!linkURL) throw new Error('could not mint a one-time dashboard link:\n' + link);
const url = linkURL.replace(/127\.0\.0\.1:\d+/, '127.0.0.1:' + port);

const browser = await chromium.launch();
const width = Number(process.env.POPULATE_W || 1600);
const height = Number(process.env.POPULATE_H || 1000);
const page = await browser.newPage({ viewport: { width: width, height: height }, colorScheme: scheme });
await page.goto(url, { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(3000);

// --- real traffic, through the real controls ---
const shotFile = path.join(tmp, 'payload.txt');
fs.writeFileSync(shotFile, 'the quick brown fox jumps over the lazy dog\n'.repeat(3));
const imgFile = path.join(tmp, 'screenshot.png');
// A 2x2 PNG, written by hand so the repo needs no image dependency.
fs.writeFileSync(imgFile, Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAFElEQVR4nGP8z4AATAxIYFRAQEAAD//wEFA/9rL8BAAAAAElFTkSuQmCC', 'base64'));

await page.evaluate(`(function(){
  document.getElementById('textPayload').value = 'Here is a code snippet to send across the wire.';
  document.querySelector('[data-action="send-text"]').click();
  return 'ok';
})()`);
await page.waitForTimeout(2500);

await page.setInputFiles('#fileInput', imgFile);
await page.waitForTimeout(900);
await page.evaluate(`(function(){
  var b = document.getElementById('btnConfirmSend');
  if (b) b.click();
  return 'ok';
})()`);
await page.waitForTimeout(2500);

// A relay attempt: it will not reach a real provider, but it should leave
// a record in the authorizations list and a line in the log.
await page.evaluate(`(function(){
  document.querySelector('[data-tab="tab-relay"]').click();
  var i = document.getElementById('oauthUrlInput');
  i.value = 'https://accounts.google.com/o/oauth2/v2/auth?client_id=demo&scope=openid';
  document.getElementById('btnRelay').click();
  return 'ok';
})()`);
await page.waitForTimeout(2500);

// A toast: copying a received snippet is the one action on the page
// that produces one.
await page.evaluate(`(function(){
  var b = document.querySelector('[data-action="copy-snippet"]');
  if (b) b.click();
  return 'ok';
})()`);
await page.waitForTimeout(900);

// And a toast the product raises on its own, so the shape of the
// notification surface is visible in the capture.
await page.evaluate(`(function(){
  if (typeof notify === 'function') {
    notify('error', 'Could not reach the destination. No data was sent.',
      { next: 'Check the peer is online and paired, then retry.' });
  }
  return 'ok';
})()`);
await page.waitForTimeout(500);

const tabs = ['tab-drop', 'tab-relay', 'tab-peers', 'tab-transfers', 'tab-logs'];
for (const t of tabs) {
  await page.evaluate(`document.querySelector('[data-tab="${t}"]').click()`);
  await page.waitForTimeout(900);
  await page.screenshot({ path: path.join(outDir, 'pop-' + t + '-' + scheme + '.png') });
}

// The populated states that only exist at a moment: the pairing dialog and
// the pairing-approval banner.
await page.evaluate(`document.querySelector('[data-tab="tab-peers"]').click()`);
await page.waitForTimeout(500);
await page.evaluate(`document.querySelector('[data-action="open-pair-modal"]').click()`);
await page.waitForTimeout(600);
await page.screenshot({ path: path.join(outDir, 'pop-modal-' + scheme + '.png') });

await browser.close();
try { hub.kill(); } catch (_) {}
try { fs.rmSync(tmp, { recursive: true, force: true }); } catch (_) {}
console.log('captured ' + tabs.length + ' tabs (' + scheme + ')');

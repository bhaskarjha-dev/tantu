// Visual review helper for the node's two pages: the QuickDrop page `tantu
// drop` serves and the relay page `tantu relay` serves.
//
// The dashboard has populated.mjs, which seeds realistic state and captures
// every tab. The node's pages had nothing: they are the surfaces nobody
// looks at, which is how their empty state came to be an unstyled
// paragraph of body text and their only control came to be unreachable
// with a keyboard.
//
// This seeds the state a reader would actually meet -- a page with a peer
// connected, a file chosen, a snippet sent, a drop received -- and captures
// each surface in both themes at every width that matters.
//
// Not part of the acceptance suite. It exists so that a change to the
// node's pages can be LOOKED at rather than only asserted about.
//
//   node tools/uxtest/nodepages.mjs <out-dir>
//
// TANTU_TEST_SHOTDIR is honoured too, so the captures can survive the run.

import { chromium } from 'playwright';
import { spawn, execFileSync } from 'child_process';
import fs from 'fs';
import os from 'os';
import path from 'path';

const OUT = process.argv[2] || path.join(os.tmpdir(), 'tantu-nodepages');
const REPO = path.resolve(path.dirname(new URL(import.meta.url).pathname).replace(/^\/([A-Za-z]:)/, '$1'), '..', '..');

const WIDTHS = [[1280, 900], [768, 900], [360, 780], [320, 720]];
const THEMES = ['dark', 'light'];

fs.mkdirSync(OUT, { recursive: true });

const EXE = path.join(OUT, 'tantu.exe');
execFileSync('go', ['build', '-o', EXE, './cmd/tantu'], { cwd: REPO, stdio: 'inherit' });

// Start a node command and wait for it to print its one-time link.
async function startNode(args, port) {
  const proc = spawn(EXE, args, { stdio: ['ignore', 'pipe', 'pipe'] });
  let out = '';
  proc.stdout.on('data', (d) => { out += d; });
  proc.stderr.on('data', (d) => { out += d; });
  const link = await new Promise((res) => {
    const iv = setInterval(() => {
      const m = out.match(/http:\/\/([^\s]*?)\/#bootstrap=([a-f0-9]+)/);
      if (m) { clearInterval(iv); res('http://' + m[1] + '/#bootstrap=' + m[2]); }
    }, 150);
    setTimeout(() => { clearInterval(iv); res(null); }, 20000);
  });
  if (!link) throw new Error('no link printed:\n' + out);
  return { proc, link };
}

const drop = await startNode(['drop', '--transport', 'loopback', '--port', '19875'], 19875);
const relay = await startNode(['relay', '--transport', 'loopback', '--port', '19876'], 19876);

const browser = await chromium.launch();

async function capture(name, link, seed) {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  await page.goto(link, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1600);
  if (seed) { await seed(page); await page.waitForTimeout(500); }

  for (const theme of THEMES) {
    await page.emulateMedia({ colorScheme: theme });
    for (const [w, h] of WIDTHS) {
      await page.setViewportSize({ width: w, height: h });
      await page.waitForTimeout(350);
      await page.screenshot({
        path: path.join(OUT, `${name}-${theme}-${w}.png`),
        fullPage: true,
      });
    }
  }
  await page.close();
}

// The QuickDrop page as a reader FIRST meets it: a peer connected, and a
// list that has not received anything yet. This is the state that was
// never looked at -- and the empty state is what it exposed: the page
// used a class that no stylesheet it could reach defined, so it rendered
// as an ordinary paragraph of body text.
await capture('drop-empty', drop.link, null);

// The QuickDrop page in use: a snippet sent, a file chosen and waiting,
// and one drop already received.
await capture('drop', drop.link, async (page) => {
  await page.evaluate(() => {
    document.getElementById('textLabel').value = 'API key';
    document.getElementById('textContent').value = 'sk-live-2f8a91c4';
    document.getElementById('selectedFileName').textContent = 'release-notes.pdf (412 KB)';
    document.getElementById('btnSendFile').disabled = false;
    setStatus('textStatus', 'Sent successfully!', 'success');
    // A received drop, drawn by the page's own renderer.
    const list = document.getElementById('receivedList');
    const es = document.getElementById('emptyState');
    if (es) es.style.display = 'none';
    const item = document.createElement('div');
    item.className = 'received-item';
    item.innerHTML =
      '<div class="received-item-header">' +
      '<span class="received-item-title">Text (deploy key)</span>' +
      '<span>18:12</span></div>' +
      '<div class="received-item-content">-----BEGIN KEY-----\n9f2a91c4…\n-----END KEY-----</div>' +
      '<div class="received-item-actions"><button class="btn btn-sm">Copy</button></div>';
    list.insertBefore(item, list.firstChild);
  });
});

// The relay page: the bookmarklet, the manual field, and a live status.
await capture('relay', relay.link, async (page) => {
  await page.evaluate(() => {
    showStatus('relaying', 'Relaying', 'accounts.google.com — waiting for the sign-in to finish.');
  });
});

await browser.close();
drop.proc.kill();
relay.proc.kill();
console.log('wrote captures to ' + OUT);

// Visual review helper: loads the running dashboard and captures it.
//
// Not part of the acceptance suite. It exists so that a change to the
// dashboard's appearance can be LOOKED at rather than only asserted
// about -- the harness proves the page is not broken, it cannot say
// whether the page is any good.
//
//   node tools/uxtest/shot.mjs <url> <out.png> [width] [height] [tab] [scheme]
//
import { chromium } from 'playwright';
import fs from 'fs';

const [, , url, out, w = '1600', h = '1000', tab = 'tab-drop', scheme = 'dark'] = process.argv;

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: +w, height: +h }, colorScheme: scheme });
// The dashboard polls the Hub continuously, so "networkidle" never
// arrives. Wait for the document and then for the page's own settle.
await page.goto(url, { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(2500);
if (tab && tab !== '-') {
  await page.evaluate(`document.querySelector('[data-tab="${tab}"]').click()`);
  await page.waitForTimeout(700);
}
await page.screenshot({ path: out, fullPage: false });
await browser.close();
console.log('wrote ' + out + ' (' + fs.statSync(out).size + ' bytes)');

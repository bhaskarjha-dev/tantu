'use strict';

// When is the dashboard's boot actually finished?
//
// The harness used to wait for one thing: `location.hash === ''`. The comment
// above that loop claimed "the fragment is gone means the page is done booting".
// That claim is false, and the product is why.
//
// dashboard.html:1683-1690 strips the fragment and only then reloads:
//
//     history.replaceState(null, '', location.pathname + location.search);
//     window.location.reload();
//
// So there is a guaranteed window in which the fragment is gone and the boot is
// not finished: the live document is still the pre-reload one, which has no
// session cookie, which therefore correctly renders no data. The harness reads
// its first-useful-render stamp from that document, finds nothing, and reports
//
//     FAIL the document stamps a first-useful-render instant
//          nothing stamped __tantuFirstUsefulMs; ... so the Hub never gave the page one
//
// which blames the Hub for the harness having read the wrong document.
//
// A reload on a fast local machine closes that window before the next poll. A
// reload on a loaded CI runner does not, which is why this appeared on webkit
// only and on roughly one run in ten, and why it could never be reproduced
// locally: 107/107 webkit checks pass on a developer machine.
//
// "Is the hash empty" therefore cannot identify the settled document. The
// fragment identifies it: only the pre-bootstrap document ever sees one. The
// init script in run.mjs records it as __tantuBootFragment, and the settled
// document is the one that never saw a token.
//
// This module holds the condition alone so selfcheck-settle.cjs can hold it to
// account by driving a real browser, rather than by asking a function whether
// it agrees with itself.

// The fragment key the product reads (dashboard.html:1660).
const BOOT_KEY = 'tantu_bootstrap';

/**
 * Decide whether the live document is the post-bootstrap one.
 *
 * @param {{hash: string|null, bootFragment: string|null}} state
 * @returns {boolean} true when the document under test saw no bootstrap token,
 *   which is to say it is the document that survived the reload.
 */
function bootSettled(state) {
  if (!state) return false;
  if (typeof state.hash !== 'string' || state.hash !== '') return false;
  // A document that saw a token is the pre-reload one, whatever its hash says.
  // The failure branch matters: `bootFragment` is null when the read failed, and
  // treating that as "settled" would reintroduce exactly the bug this fixes.
  if (state.bootFragment !== null) return false;
  return true;
}

/**
 * Explain a state that did not settle, for the failure message.
 *
 * @param {{hash: string|null, bootFragment: string|null, docId: string|null}} state
 * @returns {string} which document the harness is actually looking at.
 */
function describeBootState(state) {
  const s = state || {};
  const doc = s.docId ? 'doc ' + String(s.docId).slice(0, 8) : 'an unreadable document';
  if (s.hash !== '') {
    return 'the fragment is still present (' + JSON.stringify(s.hash) + '), so the '
      + 'bootstrap exchange has not finished';
  }
  if (s.bootFragment !== null) {
    return 'the fragment is gone but the live document is the pre-reload one (it saw '
      + 'tantu_bootstrap=' + String(s.bootFragment) + '), which has no session cookie '
      + 'and so correctly renders no data -- waiting for the reload to land';
  }
  if (s.docId === null || s.docId === undefined) {
    return 'the document could not be read, so the harness cannot tell whether the '
      + 'reload has landed';
  }
  return doc + ' settled';
}

module.exports = { bootSettled, describeBootState, BOOT_KEY };
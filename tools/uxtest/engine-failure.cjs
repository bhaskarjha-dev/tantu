// Recognising when the *engine* failed a request, rather than the Hub answering
// wrongly.
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
// A separate module, not a function inside run.mjs, so that it can be
// self-checked the way tools/uxtest/selfcheck-*.cjs already are. A detector that
// cannot be tested is a detector that quietly stops matching.

// The phrasing actually observed on the runner. Other engines' phrasings are to
// be added when one is seen failing, not guessed at in advance: a pattern loose
// enough to catch a legitimate resource failure would mislabel a product defect
// as an engine defect, which is worse than saying nothing at all.
const INTERNAL_ERROR = /encountered an internal error/i;
const RESOURCE_FAILURE = /Failed to load resource/i;

// isEngineFailure reports whether a console entry describes the engine failing to
// load something, rather than the server responding with an error.
//
// Both patterns are required. "Encountered an internal error" alone could come
// from anywhere, including the page's own script; the resource-failure prefix is
// what makes it about a fetch.
function isEngineFailure(entry) {
  const s = String(entry ?? '');
  return RESOURCE_FAILURE.test(s) && INTERNAL_ERROR.test(s);
}

// noteEngineFailure records the first engine failure seen, and only the first:
// five failures with one cause should read as one cause, not as five.
//
// The recorded note is also readable as `note.last`, because the consumer needs it
// at the moment a check fails, which is not the moment the entry arrived.
function createEngineFailureNote() {
  // `recorded`, not `note`: a named function expression binds its own name inside
  // its own body, so writing `function note(...)` and then reading `note` in that
  // body means the *function*, which is never null -- so the first call returned
  // the function for every entry, including entries that are not engine failures.
  // The self-check caught this on its first run, which is the only argument for
  // having written one.
  let recorded = null;
  const fn = function note(entry) {
    if (recorded !== null) return recorded;
    if (!isEngineFailure(entry)) return null;
    recorded = String(entry).slice(0, 160);
    fn.last = recorded;
    return recorded;
  };
  fn.last = null;
  return fn;
}

module.exports = { isEngineFailure, createEngineFailureNote };

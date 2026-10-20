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

// classifyOutcome reports the exit code a finished run should carry.
//
//	0  every check passed
//	1  the product failed a check
//	3  the harness itself failed
//	4  the Hub under test exited mid-run, or too few checks ran to mean anything
//	5  the browser engine failed a request, so this run says nothing about the
//	   product
//
// The engine case is deliberately two-sided, and both sides matter.
//
// An engine failure with every check passing is not a fault worth failing on:
// the engine recovered, and the run measured the product. A run with failures
// and no engine failure is a product failure, and stays exit 1 -- the engine
// note must never become a way to explain away a product defect. Only a run
// that both failed and saw the engine fail is exit 5.
//
// This is the classification the harness could not make before. The detector
// existed and labelled each failure with the engine note, so a reader could
// see the attribution -- but the run still exited 1, and the job still read as
// "the product broke". On 2026-10-20 the webkit peers run on the Linux runner
// reported seven failures, five of them carrying the engine note, and the job
// said the product was broken.
//
// The Hub-died case is checked first because a Hub that exited is either a
// product defect or a broken environment, and both of those are worth a red
// build; the engine case is the one where the product is demonstrably fine.
function classifyOutcome(o) {
  const outcome = o || {};
  const failures = Number(outcome.failures) || 0;
  // Too few checks is not a result at all, whatever the checks said.
  if (outcome.tooFewChecks) return 4;
  if (failures === 0) return 0;
  // A Hub that exited mid-run is either a product defect or a broken
  // environment, and both of those are worth a red build.
  if (outcome.hubDeath) return 4;
  // The engine note must never become a way to explain away a product
  // defect: it only classifies a run that already failed.
  if (outcome.engineFailureObserved === true) return 5;
  return 1;
}

module.exports = { isEngineFailure, createEngineFailureNote, classifyOutcome };

// Self-check for the engine-failure detector.
//
// A detector that cannot be tested is a detector that quietly stops matching, and
// the consequence is the worst kind: the run keeps reporting every downstream
// failure individually and nothing ever says they share a cause. That is
// exactly the failure this detector was written to prevent.
//
// This mirrors the shape of selfcheck-overflow-probe.cjs: the harness asserts the
// harness. It is deliberately small, and it is deliberately not satisfied by the
// detector agreeing with itself.
'use strict';

const { isEngineFailure, createEngineFailureNote, classifyOutcome } = require('./engine-failure.cjs');

const failures = [];

// The entry that actually occurred on 2026-10-16, webkit, peerless, commit
// fd85456. Copied verbatim from the CI annotation: a detector tested only on
// entries invented here would be tested on the author's idea of the string.
const OBSERVED =
  'console: Failed to load resource: WebKit encountered an internal error @ ' +
  'http://127.0.0.1:18976/api/transfers/recent';

// Entries that must NOT be classified as engine failures, because each is
// something the product or the harness did on purpose.
const MUST_NOT_MATCH = [
  // The staleness check deliberately blocks /api/status. The harness filters
  // this one by path, but a detector that matched it would blame the engine for
  // the harness's own interference.
  'console: Failed to load resource: http://127.0.0.1:9876/api/status',
  // A server answering with an error is the server's answer, not an engine
  // failure. This is the distinction the whole module exists to keep.
  'console: Failed to load resource: the server responded with a status of 401 (Unauthorized) @ http://127.0.0.1:9876/api/logs',
  'console: Failed to load resource: the server responded with a status of 500 (Internal Server Error) @ http://127.0.0.1:9876/api/status',
  // Real product errors, which must keep reporting as product errors.
  'pageerror: TypeError: cannot read properties of undefined (reading "render")',
  'console: WebKit encountered an internal error',          // no resource context
  'console: Failed to load resource @ http://example.com/x', // no internal error
  '',
  null,
  undefined,
];

if (!isEngineFailure(OBSERVED)) {
  failures.push(
    'the detector does not recognise the entry that actually occurred on ' +
    '2026-10-16, so every webkit failure downstream of it would report as an ' +
    'independent product defect again: ' + OBSERVED);
}

for (const entry of MUST_NOT_MATCH) {
  if (isEngineFailure(entry)) {
    failures.push(
      'the detector classifies as an engine failure an entry it must not: ' +
      JSON.stringify(entry) + '. Either it would blame the engine for the ' +
      'harness\'s own blocked /api/status poll, or it would relabel a real ' +
      'product error as an engine problem, which is worse than saying nothing');
  }
}

// Only the first cause is kept, so five failures with one cause read as one.
const note = createEngineFailureNote();
if (note('console: something unrelated') !== null) {
  failures.push('an unrelated entry produced an engine-failure note');
}
const first = note(OBSERVED);
if (typeof first !== 'string' || first.length === 0) {
  failures.push('the note for the observed entry was empty');
}
const second = note(OBSERVED + ' @ http://127.0.0.1:18976/api/status');
if (second !== first) {
  failures.push(
    'a second engine failure replaced the first note, so the run would report ' +
    'several unrelated causes for what is one');
}

// The classification: what a finished run should be called.
//
// The detector labels a failure; the classification decides what the *run* is.
// Without it the run exits 1 and the job reads as "the product broke" even when
// the browser is the thing that broke -- which is what happened on 2026-10-20.
const CLASSIFICATIONS = [
  {
    name: 'every check passed',
    outcome: { failures: 0 },
    want: 0,
    why: 'a green run is a green run, whatever the engine did',
  },
  {
    name: 'a product failure with no engine fault',
    outcome: { failures: 3 },
    want: 1,
    why: 'the engine note must never become a way to explain away a product defect',
  },
  {
    name: 'a product failure alongside an engine fault',
    outcome: { failures: 3, engineFailureObserved: true },
    want: 5,
    why: 'this is the case the run could not previously state: the browser failed, ' +
      'so the run measured the browser and not the product',
  },
  {
    name: 'an engine fault with no failures',
    outcome: { failures: 0, engineFailureObserved: true },
    want: 0,
    why: 'the engine recovered, and the run still measured the product',
  },
  {
    name: 'the Hub exited mid-run',
    outcome: { failures: 3, hubDeath: true },
    want: 4,
    why: 'a Hub that exited is either a product defect or a broken environment, and both are worth a red build',
  },
  {
    name: 'the Hub exited mid-run alongside an engine fault',
    outcome: { failures: 3, hubDeath: true, engineFailureObserved: true },
    want: 4,
    why: 'the Hub exiting is the more specific cause, and it is checked first',
  },
  {
    name: 'too few checks to mean anything',
    outcome: { failures: 0, tooFewChecks: true },
    want: 4,
    why: 'a run that checked almost nothing is not a result, whatever the checks said',
  },
  {
    name: 'no outcome at all',
    outcome: undefined,
    want: 0,
    why: 'a missing outcome must not be read as a failure',
  },
];

for (const c of CLASSIFICATIONS) {
  const got = classifyOutcome(c.outcome);
  if (got !== c.want) {
    failures.push(
      'the run would be classified as exit ' + got + ' where it should be exit ' + c.want +
      ' (' + c.name + '): ' + c.why);
  }
}

if (failures.length) {
  for (const f of failures) console.log('  FAIL  ' + f);
  console.log('\nuxtest selfcheck: engine-failure detection is not trustworthy');
  process.exit(1);
}

console.log(
  'uxtest selfcheck: the engine-failure detector matches the entry observed on the ' +
  'runner, does not match a server error, a blocked poll, or a page error, keeps ' +
  'one cause as one cause, and classifies a finished run so that a browser fault is ' +
  'never reported as a product defect');

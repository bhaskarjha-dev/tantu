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

const { isEngineFailure, createEngineFailureNote } = require('./engine-failure.cjs');

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

if (failures.length) {
  for (const f of failures) console.log('  FAIL  ' + f);
  console.log('\nuxtest selfcheck: engine-failure detection is not trustworthy');
  process.exit(1);
}

console.log(
  'uxtest selfcheck: the engine-failure detector matches the entry observed on the ' +
  'runner, does not match a server error, a blocked poll, or a page error, and keeps ' +
  'one cause as one cause');

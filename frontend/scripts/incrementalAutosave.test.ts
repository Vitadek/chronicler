/**
 * Dirty-chapter autosave payload contract.
 *
 * The autosave hook (useManuscriptAutosave) used to PUT the entire manuscript
 * on every save. It now diffs against the last authoritative server response
 * and sends only what actually changed — this proves the diff itself
 * (chaptersForRequest/baselineFromManuscript), not the hook's stateful
 * scheduling, which needs a real React render cycle and is out of scope for
 * a plain script test.
 *
 * Run: npx tsx scripts/incrementalAutosave.test.ts
 */
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';

// useManuscriptAutosave's sibling imports (manuscriptService, the draft
// journal) touch window/localStorage at module load time — same pattern as
// pluginNullableResponse.test.ts.
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
(globalThis as any).window = dom.window;
(globalThis as any).localStorage = dom.window.localStorage;
(globalThis as any).sessionStorage = dom.window.sessionStorage;
(globalThis as any).CustomEvent = dom.window.CustomEvent;
// Node 22 exposes a built-in, getter-only `navigator`; direct assignment
// throws in ESM strict mode (see schemaRoundTrip.test.ts's identical note).
Object.defineProperty(globalThis, 'navigator', {
  value: dom.window.navigator,
  configurable: true,
});

const { chaptersForRequest, baselineFromManuscript } = await import('../src/hooks/useManuscriptAutosave');
import type { Manuscript } from '../src/types';

let failures = 0;
function check(name: string, fn: () => void) {
  try {
    fn();
    console.log(`  ok  ${name}`);
  } catch (err) {
    failures++;
    console.error(`FAIL  ${name} — ${err instanceof Error ? err.message : err}`);
  }
}

function manuscript(chapters: { id: string; title: string; content: string }[]): Manuscript {
  return {
    metadata: { id: 'ms', title: 'Book', author: 'Author', lastModified: 0 },
    chapters: chapters.map((c) => ({ ...c, lastModified: 0 })),
  } as Manuscript;
}

// ── No baseline: full send, every chapter stamped with its true position ────
check('with no baseline, every chapter is sent with its true index as position', () => {
  const m = manuscript([
    { id: 'a', title: 'A', content: '<p>a</p>' },
    { id: 'b', title: 'B', content: '<p>b</p>' },
  ]);
  const out = chaptersForRequest(m, null, null);
  assert.equal(out.length, 2);
  assert.deepEqual(out.map((c) => c.position), [0, 1]);
});

// ── With a baseline: only dirty chapters are sent ────────────────────────────
check('with a baseline, only the changed chapter is sent', () => {
  const before = manuscript([
    { id: 'a', title: 'A', content: '<p>a</p>' },
    { id: 'b', title: 'B', content: '<p>b</p>' },
  ]);
  const baseline = baselineFromManuscript(before);

  const after = manuscript([
    { id: 'a', title: 'A', content: '<p>a</p>' },
    { id: 'b', title: 'B', content: '<p>b revised</p>' },
  ]);
  const out = chaptersForRequest(after, null, baseline);
  assert.equal(out.length, 1);
  assert.equal(out[0].id, 'b');
  assert.equal(out[0].position, 1);
});

check('an unchanged manuscript against its own baseline sends nothing', () => {
  const m = manuscript([
    { id: 'a', title: 'A', content: '<p>a</p>' },
    { id: 'b', title: 'B', content: '<p>b</p>' },
  ]);
  const baseline = baselineFromManuscript(m);
  const out = chaptersForRequest(m, null, baseline);
  assert.equal(out.length, 0);
});

check('a brand-new chapter not in the baseline is always sent', () => {
  const before = manuscript([{ id: 'a', title: 'A', content: '<p>a</p>' }]);
  const baseline = baselineFromManuscript(before);
  const after = manuscript([
    { id: 'a', title: 'A', content: '<p>a</p>' },
    { id: 'c', title: 'New', content: '<p>new</p>' },
  ]);
  const out = chaptersForRequest(after, null, baseline);
  assert.equal(out.length, 1);
  assert.equal(out[0].id, 'c');
  assert.equal(out[0].position, 1);
});

// ── The collab exclusion + position-shift bug ───────────────────────────────
check('the collab-excluded chapter is never sent, but its neighbors keep their true (unshifted) position', () => {
  const m = manuscript([
    { id: 'a', title: 'A', content: '<p>a</p>' },
    { id: 'collab', title: 'Collab', content: '<p>owned by collab</p>' },
    { id: 'c', title: 'C', content: '<p>c</p>' },
  ]);
  // No baseline: full send, but excluding "collab".
  const out = chaptersForRequest(m, 'collab', null);
  assert.equal(out.length, 2);
  assert.deepEqual(out.map((c) => c.id), ['a', 'c']);
  // True indices in the ORIGINAL array (0 and 2), not re-numbered by their
  // position in the smaller filtered array (which would be 0 and 1) — this
  // is the bug a payload without explicit positions used to have.
  assert.deepEqual(out.map((c) => c.position), [0, 2]);
});

check('deleting a chapter shifts later chapters to position-dirty even with a baseline', () => {
  const before = manuscript([
    { id: 'a', title: 'A', content: '<p>a</p>' },
    { id: 'b', title: 'B', content: '<p>b</p>' },
    { id: 'c', title: 'C', content: '<p>c</p>' },
  ]);
  const baseline = baselineFromManuscript(before);

  // "b" deleted through the app's own explicit delete flow (not modeled
  // here — this test only cares that its absence from the array shifts "c"'s
  // index, and that alone should mark "c" dirty against the baseline).
  const after = manuscript([
    { id: 'a', title: 'A', content: '<p>a</p>' },
    { id: 'c', title: 'C', content: '<p>c</p>' },
  ]);
  const out = chaptersForRequest(after, null, baseline);
  assert.equal(out.length, 1);
  assert.equal(out[0].id, 'c');
  assert.equal(out[0].position, 1);
});

if (failures > 0) {
  console.error(`\n${failures} incremental-autosave check(s) failed`);
  process.exit(1);
}
console.log('\nAll incremental-autosave checks passed.');

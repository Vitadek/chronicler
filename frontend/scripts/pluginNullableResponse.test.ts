/** Regression: Settings must open against the installed Proofreader payload
 * emitted by older Go builds, where empty collections were JSON `null`. */
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';

const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
(globalThis as any).window = dom.window;
(globalThis as any).localStorage = dom.window.localStorage;
(globalThis as any).sessionStorage = dom.window.sessionStorage;
(globalThis as any).CustomEvent = dom.window.CustomEvent;

const legacyProofreaderPayload = {
  plugins: [{
    id: 'chronicle.proofreader',
    name: 'Proofreader',
    description: 'Guided spelling and grammar review.',
    version: '2.0.0',
    source: 'git',
    enabled: true,
    state: '{}',
    provides: null,
    requires: ['host:languagetool'],
    wants: null,
    conflicts: null,
    replaces: ['core:proofreader'],
    dependencies: null,
    incoming: null,
    missingReasons: null,
    unmetWantsReasons: null,
    status: { missing: null, unmetWants: null, conflictsWith: null },
  }],
  hostCapabilities: ['host:languagetool'],
  shadowedCore: null,
  activationOrder: ['chronicle.proofreader'],
};

(globalThis as any).fetch = async () => new Response(JSON.stringify(legacyProofreaderPayload), { status: 200 });

const { pluginService } = await import('../src/services/pluginService');
const state = await pluginService.list();
const proofreader = state.plugins[0];

assert.deepEqual(state.shadowedCore, []);
assert.deepEqual(proofreader.provides, []);
assert.deepEqual(proofreader.wants, []);
assert.deepEqual(proofreader.conflicts, []);
assert.deepEqual(proofreader.dependencies, {});
assert.deepEqual(proofreader.status, { missing: [], unmetWants: [], conflictsWith: [] });
assert.doesNotThrow(() => {
  proofreader.replaces.map(String);
  proofreader.missingReasons.map(String);
  proofreader.status.conflictsWith.map((item) => item.pluginId);
});

console.log('Nullable legacy Proofreader collections normalize before Settings renders.');

import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const root = resolve(import.meta.dirname, '..');
const removed = [
  'src/components/AiSettingsPanel.tsx',
  'src/components/IssuesPane.tsx',
  'src/components/SmartThesaurus.tsx',
  'src/lib/AiGrammar.ts',
  'src/lib/TenseShift.ts',
  'src/lib/tense/detect.ts',
  'src/services/aiConfig.ts',
  'src/services/aiService.ts',
  'src/services/grammarAiService.ts',
];
for (const path of removed) {
  assert.equal(existsSync(resolve(root, path)), false, `${path} must not ship in core`);
}

const toolbar = readFileSync(resolve(root, 'src/components/FormattingToolbar.tsx'), 'utf8');
for (const label of ['Bold', 'Italic', 'Underline']) {
  assert.match(toolbar, new RegExp(`title: '${label}'`), `${label} remains in the selection toolbar`);
}
assert.doesNotMatch(toolbar, /thesaurus|gemini|\/api\/ai/i);

const pluginApi = readFileSync(resolve(root, 'src/plugins/api/index.ts'), 'utf8');
assert.match(pluginApi, /PLUGIN_API_VERSION = 4/);
assert.doesNotMatch(pluginApi, /host:ai|host:gemini|services\.ai/);

console.log('Core removal contract and formatting-only toolbar verified.');

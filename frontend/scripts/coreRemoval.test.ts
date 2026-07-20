import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const root = resolve(import.meta.dirname, '..');
const removed = [
  'src/components/AiSettingsPanel.tsx',
  'src/components/IssuesPane.tsx',
  'src/components/SmartThesaurus.tsx',
  'src/components/OutlinePane.tsx',
  'src/components/CharacterSheet.tsx',
  'src/components/PlotCanvas.tsx',
  'src/components/CommentsPanel.tsx',
  'src/components/PopoutWindow.tsx',
  'src/lib/AiGrammar.ts',
  'src/lib/Autocomplete.ts',
  'src/lib/AutoCorrect.ts',
  'src/lib/autocomplete/engine.ts',
  'src/lib/autocomplete/wordlist.ts',
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
assert.doesNotMatch(pluginApi, /core:outliner/);
assert.doesNotMatch(pluginApi, /core:(?:grammar|autocorrect)/);
assert.match(pluginApi, /host:grammar/, 'the Proofreader host grammar service remains available');

const sidebar = readFileSync(resolve(root, 'src/components/Sidebar.tsx'), 'utf8');
assert.doesNotMatch(sidebar, />\s*Outline\s*</);
assert.doesNotMatch(sidebar, /view === ['"]outline['"]|OutlinePane|core:outliner/);
for (const removedLabel of ['Autocomplete', 'Grammar Check', 'Autocorrect']) {
  assert.ok(!sidebar.includes(`>${removedLabel}<`) && !sidebar.includes(`\n                              ${removedLabel}\n`), `${removedLabel} setting is absent`);
}
assert.doesNotMatch(sidebar, /core:(?:grammar|autocorrect)/);

const app = readFileSync(resolve(root, 'src/App.tsx'), 'utf8');
assert.doesNotMatch(app, /core:outliner|chronicle_(?:chars|plotnodes|plotedges)_/);
assert.doesNotMatch(app, /chronicle_(?:autocomplete|autocorrect|grammar_check)|isAutocompleteEnabled|isAutoCorrectEnabled|isGrammarCheckEnabled/);

const settingsSync = readFileSync(resolve(root, 'src/lib/settingsSync.ts'), 'utf8');
const syncedSettingsList = settingsSync.slice(
  settingsSync.indexOf('export const SYNCED_SETTINGS_KEYS'),
  settingsSync.indexOf('] as const;'),
);
assert.doesNotMatch(syncedSettingsList, /chronicle_(?:autocomplete|autocorrect|grammar_check)/);

const editorHook = readFileSync(resolve(root, 'src/hooks/useChronicleEditor.ts'), 'utf8');
assert.doesNotMatch(editorHook, /Autocomplete|AutoCorrect|setGrammarCheck|isGrammarCheckEnabled/);

const editorExtensions = readFileSync(resolve(root, 'src/lib/editorExtensions.ts'), 'utf8');
assert.doesNotMatch(editorExtensions, /import \{ AutoCorrect \}|\bAutoCorrect,|Autocomplete ghost/);

const types = readFileSync(resolve(root, 'src/types.ts'), 'utf8');
for (const legacyField of ['characters?:', 'plotNodes?:', 'plotEdges?:']) {
  assert.ok(types.includes(legacyField), `${legacyField} remains readable for legacy documents`);
}

console.log('Core removal contract and formatting-only toolbar verified.');

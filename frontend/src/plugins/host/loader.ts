import React from 'react';
import * as ReactJsxRuntime from 'react/jsx-runtime';
import * as ReactDOM from 'react-dom';
import * as TiptapCore from '@tiptap/core';
import * as TiptapReact from '@tiptap/react';
import * as TiptapReactMenus from '@tiptap/react/menus';
import * as TiptapState from '@tiptap/pm/state';
import * as TiptapViewNs from '@tiptap/pm/view';
import * as TiptapModel from '@tiptap/pm/model';
import * as Motion from 'motion/react';
import * as PluginApi from '../api';
import { PLUGIN_API_VERSION, type ChroniclePlugin } from '../api';
import { authFetch } from '../../services/authService';

/**
 * Loads a compiled plugin module into the running app.
 *
 * The server builds each plugin with esbuild as **CommonJS**, leaving react,
 * @tiptap/*, motion and the plugin API **external**. We evaluate that code with
 * a `require` shim bound to the app's OWN module instances — so a plugin shares
 * exactly one React and one TipTap with the host (two Reacts would crash hooks
 * instantly). This is the Obsidian/VS Code approach; it needs no import maps and
 * no enumeration of named exports.
 *
 * `new Function` means a plugin runs with full privileges. That is the accepted
 * trust model (trust-on-install, see PLUGINS.md) — the protection is that
 * installing requires an authenticated, deliberate git URL you chose.
 */

/** The modules a plugin may `import` (esbuild marks these external). */
const HOST_MODULES: Record<string, unknown> = {
  react: React,
  // esbuild's automatic JSX transform emits require("react/jsx-runtime") in
  // every file with JSX — without this, no plugin that renders can load.
  'react/jsx-runtime': ReactJsxRuntime,
  'react/jsx-dev-runtime': ReactJsxRuntime,
  'react-dom': ReactDOM,
  '@tiptap/core': TiptapCore,
  '@tiptap/react': TiptapReact,
  '@tiptap/react/menus': TiptapReactMenus,
  '@tiptap/pm/state': TiptapState,
  '@tiptap/pm/view': TiptapViewNs,
  '@tiptap/pm/model': TiptapModel,
  'motion/react': Motion,
  '@chronicle/plugin-api': PluginApi,
};

// `lucide-react` is deliberately NOT in HOST_MODULES above: it is the
// entire 1,573-icon set (~700K), so it is only fetched (as its own
// content-hashed chunk) when a plugin's compiled source actually requires
// it — see the scan in loadPluginModule. Named here so hostRequire's error
// message still advertises it as an available host module even before it
// has been loaded for this session.
const LUCIDE_MODULE_NAME = 'lucide-react';
const HOST_MODULE_NAMES = [...Object.keys(HOST_MODULES), LUCIDE_MODULE_NAME];

/** Matches the literal `require("lucide-react")` esbuild emits (see server/lib/pluginBuild.ts). */
const REQUIRES_LUCIDE = /require\(("|')lucide-react\1\)/;

function hostRequire(specifier: string): unknown {
  const mod = HOST_MODULES[specifier];
  if (!mod) {
    throw new Error(
      `Plugin required "${specifier}", which the host does not provide. ` +
      `Available: ${HOST_MODULE_NAMES.join(', ')}. ` +
      `Bundle any other dependency into your plugin instead of importing it.`,
    );
  }
  return mod;
}

/** The app version plugins declare compatibility against (manifest.minAppVersion). */
export const APP_VERSION: string =
  ((import.meta as unknown as { env?: Record<string, string> }).env?.VITE_APP_VERSION) || '0.1.0';

/** Semver-ish "is `version` >= `min`" over dot-separated numbers. */
export function satisfiesMinVersion(version: string, min: string | undefined): boolean {
  if (!min) return true;
  const a = version.split('.').map((n) => parseInt(n, 10) || 0);
  const b = min.split('.').map((n) => parseInt(n, 10) || 0);
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const x = a[i] ?? 0;
    const y = b[i] ?? 0;
    if (x > y) return true;
    if (x < y) return false;
  }
  return true;
}

/**
 * Fetch phase only — pure network, no evaluation. Split out so PluginHost can
 * kick every plugin's bundle fetch off in parallel with the './loader' +
 * 'editorExtensions' chunk imports, instead of serializing the fetch behind
 * them (see PluginHost.tsx refresh()).
 */
export async function fetchPluginModuleCode(pluginId: string): Promise<string> {
  const res = await authFetch(`/api/plugins/${encodeURIComponent(pluginId)}/module.js`);
  if (!res.ok) {
    throw new Error(`Could not fetch plugin bundle (HTTP ${res.status}). Try re-installing it.`);
  }
  return res.text();
}

/**
 * Evaluate one plugin's already-fetched compiled bundle. Only ever called for
 * ENABLED plugins (v1 eagerly imported every installed plugin, enabled or not).
 */
export async function evaluatePluginModule(pluginId: string, code: string): Promise<ChroniclePlugin> {
  // Only fetch the ~700K icon set when this plugin's compiled bundle
  // actually references it. esbuild marks lucide-react external
  // (server/lib/pluginBuild.ts), so every compiled plugin that imports it
  // emits a literal `require("lucide-react")` — a plugin that keeps the
  // `require` reference and calls it later is still covered by this
  // textual scan. Concurrent evaluatePluginModule calls awaiting the same
  // dynamic import are harmless: Vite dedupes it.
  if (!HOST_MODULES[LUCIDE_MODULE_NAME] && REQUIRES_LUCIDE.test(code)) {
    // Keep the plugin host's deliberately-complete icon namespace out of the
    // library entry graph. The query gives Rollup a distinct optional module;
    // core UI imports from `lucide-react` remain normally tree-shaken.
    // @ts-ignore -- Vite resolves query-suffixed ESM modules at build time.
    HOST_MODULES[LUCIDE_MODULE_NAME] = await import('lucide-react/dist/esm/lucide-react.js?plugin-host');
  }

  const module: { exports: Record<string, unknown> } = { exports: {} };
  try {
    // eslint-disable-next-line no-new-func
    const factory = new Function('module', 'exports', 'require', code);
    factory(module, module.exports, hostRequire);
  } catch (err) {
    throw new Error(`Plugin failed to evaluate: ${err instanceof Error ? err.message : String(err)}`);
  }

  const exported = (module.exports.default ?? module.exports) as ChroniclePlugin;
  if (!exported || typeof exported !== 'object' || !exported.id) {
    throw new Error('Plugin did not export a ChroniclePlugin (use `export default definePlugin({...})`).');
  }
  if (exported.apiVersion !== PLUGIN_API_VERSION) {
    throw new Error(
      `Plugin targets API v${exported.apiVersion}, this Chronicle provides v${PLUGIN_API_VERSION}. ` +
      `Update the plugin (or Chronicle).`,
    );
  }
  return exported;
}

/**
 * Fetch + evaluate one plugin's compiled bundle. Convenience wrapper over the
 * two phases above for callers that don't need to pipeline the fetch.
 */
export async function loadPluginModule(pluginId: string): Promise<ChroniclePlugin> {
  const code = await fetchPluginModuleCode(pluginId);
  return evaluatePluginModule(pluginId, code);
}

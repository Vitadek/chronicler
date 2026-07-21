import { test, expect } from '../fixtures';

/**
 * TESTPLAN.md case 6: checker chunk lazy loading.
 *
 * Real targets, confirmed against a production build (web/assets/):
 *  - Plugin checkers only fetch /api/plugins/:id/module.js once ENABLED in
 *    Settings -> Plugins (PluginHost.tsx's refresh(), driven by activationOrder).
 *  - The core built-in Proofreader (ProofreadView) is React.lazy-loaded and
 *    confirmed absent from the entry chunk.
 *
 * Deliberately NOT asserting "no grammar-related string in the entry
 * chunk": PluginHost.tsx statically imports a small (~90 line) fetch
 * wrapper around /api/grammar/check (lib/grammar/languagetool.ts) that DOES
 * land in the entry chunk regardless of enabled plugins -- it's not the
 * checker engine (that runs server-side), so treating its presence as a
 * failure would be testing a non-bug.
 */
test('plugin checker code only loads after the plugin is enabled', async ({ page }) => {
  const moduleRequests: string[] = [];
  page.on('request', (req) => {
    if (/\/api\/plugins\/[^/]+\/module\.js/.test(req.url())) moduleRequests.push(req.url());
  });

  await page.goto('/');
  await page.getByRole('button', { name: 'Global Settings' }).click();
  await expect(moduleRequests).toHaveLength(0);

  const enableButton = page.getByRole('button', { name: 'Enable' }).first();
  if (await enableButton.count() === 0) {
    test.skip(true, 'no installed-but-disabled plugin available in this environment to enable');
  }
  await enableButton.click();

  await expect.poll(() => moduleRequests.length, { timeout: 5_000 }).toBeGreaterThan(0);
});

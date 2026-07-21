import { test, expect, createManuscript, openSidebar, autoAcceptDialogs } from '../fixtures';

/**
 * TESTPLAN.md case 3: offline draft journal / reload recovery.
 *
 * manuscriptDraftJournal.ts persists to localStorage on a 300ms debounce.
 * Going offline doesn't stop that (it's local, not network), but it does
 * stop the 2s network PUT from ever landing -- so on reopening the
 * manuscript, App.tsx finds a draft newer than the last acknowledged server
 * save and asks via native window.confirm() whether to restore it. That's
 * not a DOM element, so this must use page.on('dialog', ...).
 *
 * Uses route-blocking of just the manuscript PUT rather than
 * context.setOffline(true) or blocking all /api/*: the app's own boot
 * sequence (settings, auth/config, auth/me, manuscript GET) needs to
 * complete on reload, and a full offline context also blocks the reload's
 * own same-origin asset requests (no service worker has installed yet in a
 * fresh test context to serve them from cache).
 *
 * The SPA has no URL-based routing (App.tsx holds the open manuscript in
 * memory, not a route), so a reload always lands back on the Library --
 * the draft-recovery check only runs when a manuscript is (re)opened, not
 * on generic app boot. This test gives the manuscript a unique title
 * (saved BEFORE going offline, so it lands normally) to reliably find and
 * reopen the same card afterward, since other specs running concurrently
 * also create "Untitled Manuscript" entries.
 */
test('an offline edit survives a reload via the local draft journal', async ({ page }) => {
  await createManuscript(page);
  await openSidebar(page);

  await page.getByRole('button', { name: 'Export' }).click();
  const uniqueTitle = `offline-recovery-${Date.now()}`;
  await page.getByLabel('Manuscript Title').fill(uniqueTitle);
  await page.waitForTimeout(2_500); // let the title PUT land before going offline
  await page.getByRole('button', { name: 'Close sidebar' }).click(); // the open sidebar's backdrop blocks the editor

  const editor = page.locator('.novel-editor-content').last();
  await editor.click();
  const marker = `offline-edit-${Date.now()}`;
  await editor.pressSequentially(marker, { delay: 30 });

  // Let the 300ms journal debounce fire before cutting the network, so the
  // draft is actually written to localStorage first.
  await page.waitForTimeout(600);
  await page.route('**/api/manuscripts/*', (route) =>
    route.request().method() === 'PUT' ? route.abort('internetdisconnected') : route.continue(),
  );

  autoAcceptDialogs(page); // must be registered before reopening triggers the confirm()
  await page.reload();
  await page.getByText(uniqueTitle).click();

  await expect(page.getByText(marker)).toBeVisible({ timeout: 5_000 });
});

import { test, expect, openSidebar } from '../fixtures';

/**
 * TESTPLAN.md case 1: stale library request suppression.
 *
 * LibraryView.tsx guards against this with a generation counter
 * (loadSequenceRef, incremented on every fetch call AND on unmount cleanup)
 * -- a response is only applied if its sequence still matches the current
 * one. We exercise the real guard, not a simulation of it: fire a slow
 * request on first mount, unmount+remount (via New Work -> Return to
 * Library) to start a second, fast request, then confirm the slow
 * request's stale response never overwrites what the second request
 * rendered, even once it finally arrives.
 */
test('a stale /api/manuscripts response never overwrites a newer one', async ({ page }) => {
  let firstRequestSeen = false;
  await page.route('**/api/manuscripts', async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    if (!firstRequestSeen) {
      firstRequestSeen = true;
      await new Promise((r) => setTimeout(r, 2500));
    }
    await route.continue();
  });

  await page.goto('/'); // request #1: slow, fires before any manuscript exists

  await page.getByRole('button', { name: 'New Work', exact: true }).click();
  await expect(page.getByText('Chapter 1')).toBeVisible();

  await openSidebar(page);
  await page.getByRole('button', { name: 'Return to Library' }).click();
  // request #2: fast (no delay applied, firstRequestSeen already true), includes the new manuscript
  await expect(page.getByText('Untitled Manuscript').first()).toBeVisible({ timeout: 5_000 });

  // Wait past request #1's 2.5s delay. If the stale-response guard failed,
  // request #1's response (fired before the manuscript existed) would land
  // now and wipe the card that request #2 correctly rendered.
  await page.waitForTimeout(3_000);
  await expect(page.getByText('Untitled Manuscript').first()).toBeVisible();
});

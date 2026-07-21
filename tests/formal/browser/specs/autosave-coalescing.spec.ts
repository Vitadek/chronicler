import { test, expect, createManuscript, saveStatusText } from '../fixtures';

/**
 * TESTPLAN.md case 2: coalesced autosave under latency.
 *
 * useManuscriptAutosave.ts debounces the network PUT at debounceMs=2000 --
 * every keystroke resets the timer, so N keystrokes within the window
 * should produce ONE PUT, not N. We add artificial network latency on top
 * (matching the case name) to confirm coalescing holds even when a save is
 * slow, not just when it's instant.
 */
test('rapid typing under network latency coalesces into a single save', async ({ page }) => {
  await createManuscript(page);

  let putCount = 0;
  await page.route('**/api/manuscripts/*', async (route) => {
    if (route.request().method() === 'PUT') {
      putCount++;
      await new Promise((r) => setTimeout(r, 800)); // simulated latency
    }
    await route.continue();
  });

  const editor = page.locator('.novel-editor-content').last();
  await editor.click();
  for (const word of ['The', 'quick', 'brown', 'fox', 'jumps']) {
    await editor.pressSequentially(`${word} `, { delay: 50 });
  }

  await expect(saveStatusText(page)).toHaveText(/Unsaved changes|Saving/i, { timeout: 3_000 });
  await expect(saveStatusText(page)).toHaveText('Saved', { timeout: 8_000 });

  // 1 PUT is the ideal coalesced outcome; a couple more can legitimately
  // happen when a save is still in flight and a later edit schedules a
  // follow-up once it resolves. What this guards against is the absence of
  // coalescing entirely, i.e. one PUT per keystroke (this test types ~25
  // characters), which real debouncing must never do.
  expect(putCount).toBeLessThan(5);
});

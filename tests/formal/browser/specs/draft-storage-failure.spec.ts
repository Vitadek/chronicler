import { test, expect, createManuscript, saveStatusText } from '../fixtures';

/**
 * TESTPLAN.md case 5: a visible draft-storage failure state.
 *
 * Already fully implemented, not hypothetical: manuscriptDraftJournal.ts's
 * writeManuscriptDraft() catches localStorage.setItem throwing (e.g.
 * QuotaExceededError), returns false, and useManuscriptAutosave sets
 * status='draft-error' -- App.tsx's SaveStatus pill then shows "Local
 * draft unavailable" with a Retry button. We force the throw via an init
 * script rather than actually filling storage to quota (faster, deterministic).
 */
test('a failed local draft write surfaces as a visible error, not silently', async ({ page }) => {
  await page.addInitScript(() => {
    const realSetItem = Storage.prototype.setItem.bind(window.localStorage);
    Storage.prototype.setItem = function (key: string, value: string) {
      if (key.startsWith('chronicle_manuscript_draft_')) {
        throw new DOMException('Simulated quota exceeded', 'QuotaExceededError');
      }
      return realSetItem(key, value);
    };
  });

  await createManuscript(page);
  const editor = page.locator('.novel-editor-content').last();
  await editor.click();
  await editor.pressSequentially('this edit cannot be journaled locally', { delay: 20 });

  await expect(page.getByText('Local draft unavailable')).toBeVisible({ timeout: 5_000 });
  await expect(saveStatusText(page).getByRole('button', { name: 'Retry' })).toBeVisible();
});

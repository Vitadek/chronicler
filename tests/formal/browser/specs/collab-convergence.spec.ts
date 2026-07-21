import { test, expect } from '@playwright/test';

/**
 * TESTPLAN.md case 7: two browser-context collaboration convergence.
 *
 * Collab is opt-in (localStorage.chronicle_collab='1', read at load) and
 * scoped per-user per CollabEditor.tsx's docName format
 * (`<encoded-user>/<manuscript>:<chapter>`), so both contexts need the same
 * manuscript/chapter AND the same auth identity to land on one Y.Doc. With
 * AUTH_MODE=none (this tier's compose config) there is one implicit shared
 * identity, so two contexts naturally qualify once they open the same book.
 *
 * There's no URL-based routing in this SPA (App.tsx conditionally renders
 * views from in-memory state, not a route), so context B finds the same
 * manuscript the only way a real second user would: through the Library
 * list. LibraryView sorts by lastModified descending, so the manuscript
 * context A just created and edited is reliably the first card.
 *
 * There is no "synced" signal beyond a connectivity status string
 * (`Live · connected`), so convergence is asserted by polling text equality
 * rather than trusting that status.
 */
test('two contexts editing the same chapter converge to the same content', async ({ browser }) => {
  const contextA = await browser.newContext();
  const contextB = await browser.newContext();
  await contextA.addInitScript(() => window.localStorage.setItem('chronicle_collab', '1'));
  await contextB.addInitScript(() => window.localStorage.setItem('chronicle_collab', '1'));

  const pageA = await contextA.newPage();
  const pageB = await contextB.newPage();

  try {
    await pageA.goto('/');
    await pageA.getByRole('button', { name: 'New Work', exact: true }).click();
    const editorA = pageA.locator('[data-testid="collab-editor-content"]');
    await expect(editorA).toBeVisible({ timeout: 10_000 });

    await pageB.goto('/');
    await pageB.getByText('Untitled Manuscript').first().click();
    const editorB = pageB.locator('[data-testid="collab-editor-content"]');
    await expect(editorB).toBeVisible({ timeout: 10_000 });

    const marker = `converge-${Date.now()}`;
    await editorA.click();
    await editorA.pressSequentially(marker, { delay: 30 });

    await expect(async () => {
      const textA = await editorA.textContent();
      const textB = await editorB.textContent();
      expect(textA).toContain(marker);
      expect(textB).toContain(marker);
    }).toPass({ timeout: 10_000 });
  } finally {
    await contextA.close();
    await contextB.close();
  }
});

import { test, expect, createManuscript, openSidebar } from '../fixtures';

/**
 * TESTPLAN.md case 4: explicit chapter deletion.
 *
 * Sidebar.tsx uses a two-step inline confirm (no modal): the chapter row's
 * kebab menu (ChapterMenu.tsx, accessible via its title="More actions")
 * offers "Delete", which swaps the row for a DELETE/Cancel pair. Only
 * clicking DELETE actually removes the chapter -- Cancel (aria-label
 * "Cancel", added for this suite) must leave it untouched.
 */
test('deleting a chapter requires an explicit second confirmation', async ({ page }) => {
  await createManuscript(page);
  await openSidebar(page);
  await page.getByRole('button', { name: 'Add Chapter' }).click();
  // New chapters default to "Untitled Chapter" (only manuscript CREATION
  // seeds the literal "Chapter 1" -- see App.tsx's handleCreateNew).
  await expect(page.getByText('Untitled Chapter')).toBeVisible();

  const chapterRow = page.getByText('Untitled Chapter').locator('..');
  await chapterRow.hover();
  await page.getByRole('button', { name: 'More actions' }).last().click();
  await page.getByRole('button', { name: 'Delete' }).click();

  // Cancel path: chapter must survive.
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.getByText('Untitled Chapter')).toBeVisible();

  // Confirm path: chapter must be gone.
  await page.getByRole('button', { name: 'More actions' }).last().click();
  await page.getByRole('button', { name: 'Delete' }).click();
  await page.getByRole('button', { name: 'DELETE' }).click();
  await expect(page.getByText('Untitled Chapter')).not.toBeVisible();
});

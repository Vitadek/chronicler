import { test as base, expect, type Page, type Route } from '@playwright/test';

export { expect };

/**
 * Shared helpers for the 7 TESTPLAN.md contract cases. Kept as plain
 * functions (not custom fixtures) so each spec stays explicit about which
 * helpers it uses -- this suite is small enough that fixture indirection
 * would cost more clarity than it buys.
 */

/** Registers an auto-accept handler for the app's window.confirm() dialogs
 *  (draft recovery, conflict resolution) -- App.tsx uses native confirm(),
 *  not a DOM element, so there is no selector for these. Must be registered
 *  BEFORE the action that triggers the dialog. */
export function autoAcceptDialogs(page: Page) {
  page.on('dialog', (dialog) => dialog.accept());
}

/** Opts a page into collaborative editing before navigation -- EditorView.tsx
 *  only wires up CollabEditor when this flag is set, and it's read at load. */
export async function enableCollabMode(page: Page) {
  await page.addInitScript(() => {
    window.localStorage.setItem('chronicle_collab', '1');
  });
}

/** Delays every response matching `urlPattern` by `delayMs`, letting a real
 *  request/response still complete -- used for the coalesced-autosave and
 *  stale-request cases where we need to control response ORDER, not fake data. */
export async function delayRoute(page: Page, urlPattern: string | RegExp, delayMs: number) {
  await page.route(urlPattern, async (route: Route) => {
    await new Promise((r) => setTimeout(r, delayMs));
    await route.continue();
  });
}

/** Creates a fresh manuscript via the real UI flow (App.tsx's handleCreateNew:
 *  empty title, one chapter titled "Chapter 1", empty content) and waits for
 *  the editor to open. Returns nothing -- callers read state via the editor
 *  DOM afterward, matching how a real user would drive the app. */
export async function createManuscript(page: Page) {
  await page.goto('/');
  // exact: true -- the empty-library state also has a "Begin a New Work"
  // button, whose accessible name contains "New Work" as a substring.
  await page.getByRole('button', { name: 'New Work', exact: true }).click();
  await expect(page.getByText('Chapter 1')).toBeVisible();
}

/** The save-status pill in App.tsx's SaveStatus: aria-live="polite", plain
 *  text content ("Saved" | "Unsaved changes" | "Saving…" | ...). */
export function saveStatusText(page: Page) {
  return page.locator('[aria-live="polite"]').first();
}

/** The editor's sidebar is a collapsed drawer by default -- only a
 *  hamburger toggle (aria-label "Open sidebar"/"Close sidebar", added for
 *  this suite) is visible until opened. Chapter list, "Return to Library",
 *  and manuscript settings all live behind it. */
export async function openSidebar(page: Page) {
  await page.getByRole('button', { name: 'Open sidebar' }).click();
}

export const test = base;

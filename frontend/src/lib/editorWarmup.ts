/**
 * Starts the editor dependency graph only after a writer signals intent to
 * open a manuscript. The promise is cached so pointer, keyboard, touch, and
 * click signals all share one warm-up.
 */
let warmup: Promise<unknown> | null = null;

export function warmEditorShell(): Promise<unknown> {
  if (!warmup) {
    warmup = Promise.all([
      import('../components/EditorView'),
      import('../components/Sidebar'),
    ]);
  }
  return warmup;
}

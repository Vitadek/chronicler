/**
 * The chapter/manuscript title must always be exactly one, indestructible H1.
 *
 * The bug this pins: the title editor used the full multi-block prose schema, so
 * Ctrl+A + Delete removed the <h1> node and ProseMirror backfilled a default
 * paragraph — silently demoting the title to plain text. Enter also split the
 * title into extra blocks.
 *
 * The fix is `singleLineHeading` (src/lib/editorExtensions.ts): a Document whose
 * content is exactly `heading`, so the node can't be deleted (delete-all just
 * empties its text), Enter's splitBlock is a schema-illegal no-op, and pasted
 * multi-block content collapses to a single heading line.
 *
 * This test guards it behaviourally with a real TipTap editor, and proves the
 * OLD (unconstrained) schema would have demoted the title — so it has teeth.
 *
 * Run: npx tsx scripts/singleLineTitle.test.ts
 */
import { JSDOM } from 'jsdom';
import { register } from 'node:module';

register('./ignoreCss.loader.mjs', import.meta.url);

const dom = new JSDOM('<!doctype html><html><body></body></html>', { pretendToBeVisual: true });
(globalThis as any).window = dom.window;
(globalThis as any).document = dom.window.document;
Object.defineProperty(globalThis, 'navigator', { value: dom.window.navigator, configurable: true });
(globalThis as any).DOMParser = dom.window.DOMParser;
(globalThis as any).Node = dom.window.Node;
(globalThis as any).HTMLElement = dom.window.HTMLElement;
(globalThis as any).MutationObserver = dom.window.MutationObserver;
(globalThis as any).requestAnimationFrame = dom.window.requestAnimationFrame;
(globalThis as any).cancelAnimationFrame = dom.window.cancelAnimationFrame;
(globalThis as any).getComputedStyle = dom.window.getComputedStyle;

let failures = 0;
function check(name: string, cond: boolean, detail?: string) {
  if (cond) console.log(`  ok  ${name}`);
  else {
    failures++;
    console.error(`FAIL  ${name}${detail ? ` — ${detail}` : ''}`);
  }
}

async function main() {
  const { Editor } = await import('@tiptap/core');
  const { buildCoreExtensions } = await import('../src/lib/editorExtensions');

  const makeEditor = (title: string, singleLineHeading: boolean) =>
    new Editor({
      element: dom.window.document.createElement('div'),
      extensions: buildCoreExtensions({ singleLineHeading }),
      content: `<h1>${title}</h1>`,
    });

  const headingCount = (editor: any) => {
    let n = 0;
    editor.state.doc.forEach((node: any) => {
      if (node.type.name === 'heading') n++;
    });
    return n;
  };

  // ── A. Constrained title: select-all + delete keeps a single empty H1. ───────
  {
    const editor = makeEditor('Chapter One', true);
    check('starts as one heading', headingCount(editor) === 1 && editor.state.doc.childCount === 1);

    editor.commands.selectAll();
    editor.commands.deleteSelection();

    check('title text is emptied', editor.getText().trim() === '');
    check('still exactly one node', editor.state.doc.childCount === 1);
    check('the node is still a heading', editor.state.doc.firstChild?.type.name === 'heading');
    check('HTML has an <h1> and no <p>', /<h1/.test(editor.getHTML()) && !/<p[ >]/.test(editor.getHTML()),
      editor.getHTML());
    editor.destroy();
  }

  // ── A′. Teeth: the OLD unconstrained schema demotes the title to a paragraph. ─
  {
    const editor = makeEditor('Chapter One', false);
    editor.commands.selectAll();
    editor.commands.deleteSelection();
    check('the old schema provably backfilled a <p>', /<p[ >]/.test(editor.getHTML()),
      `old schema HTML: ${editor.getHTML()}`);
    editor.destroy();
  }

  // ── B. Enter can't split the title into extra blocks. ───────────────────────
  {
    const editor = makeEditor('Chapter One', true);
    editor.commands.focus('end');
    editor.commands.splitBlock();
    check('splitBlock adds no block (still one heading)',
      editor.state.doc.childCount === 1 && headingCount(editor) === 1);
    editor.destroy();
  }

  // ── C. Pasted / set multi-block content collapses to a single heading line. ──
  {
    const editor = makeEditor('X', true);
    editor.commands.setContent('<p>one</p><p>two</p><h2>three</h2>');
    check('multi-block content collapses to one node', editor.state.doc.childCount === 1);
    check('and that node is a heading', editor.state.doc.firstChild?.type.name === 'heading');
    check('no paragraph survives in the title', !/<p[ >]/.test(editor.getHTML()), editor.getHTML());
    editor.destroy();
  }

  console.log(failures === 0 ? '\nall checks passed' : `\n${failures} failed`);
  process.exit(failures === 0 ? 0 : 1);
}

void main();

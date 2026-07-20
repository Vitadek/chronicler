// Shared helper for the decoration-based checkers (TenseShift, Grammar):
// build a textblock's plain text alongside a char-index -> document-position
// map. Inline leaf nodes (hard breaks, atoms) advance the document position
// without contributing characters, so a naive `paraStart + offset` would drift;
// this keeps span placement exact.
import type { Node as PMNode } from '@tiptap/pm/model';

// ProseMirror nodes are immutable and unchanged paragraphs keep the same
// object identity across doc versions (edits elsewhere in the doc produce new
// wrapper nodes but reuse untouched child nodes). Cache the expensive part —
// the per-character tree walk that builds text + a position map — per node,
// keyed by paragraph-relative offsets so the same cache entry is valid no
// matter where in the document the paragraph currently starts. Absolute
// positions are cheap to derive at use time (`paraStart + rel[i]`).
const posMapCache = new WeakMap<PMNode, { text: string; rel: number[] }>();

function buildRelPosMap(node: PMNode): { text: string; rel: number[] } {
  let text = '';
  const rel: number[] = [];
  let pos = 0;
  node.forEach((child) => {
    if (child.isText) {
      const t = child.text || '';
      for (let k = 0; k < t.length; k++) {
        rel.push(pos);
        pos++;
      }
      text += t;
    } else {
      pos += child.nodeSize;
    }
  });
  rel.push(pos); // sentinel for an end offset
  return { text, rel };
}

export function buildPosMap(node: PMNode, paraStart: number): { text: string; posAt: number[] } {
  let cached = posMapCache.get(node);
  if (!cached) {
    cached = buildRelPosMap(node);
    posMapCache.set(node, cached);
  }
  const { text, rel } = cached;
  const posAt = new Array<number>(rel.length);
  for (let i = 0; i < rel.length; i++) posAt[i] = paraStart + rel[i];
  return { text, posAt };
}

/**
 * Find the first occurrence of `quote` in the editor document and return its
 * span. Retained as a general utility for plugins that map text to positions.
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function locateQuote(editor: any, quote: string): { from: number; to: number } | null {
  if (!quote) return null;
  let hit: { from: number; to: number } | null = null;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  editor.state.doc.descendants((node: any, pos: number) => {
    if (hit || !node.isTextblock) return hit ? false : undefined;
    const { text, posAt } = buildPosMap(node, pos + 1);
    const idx = text.indexOf(quote);
    if (idx >= 0) {
      const endIdx = Math.min(idx + quote.length, posAt.length - 1);
      hit = { from: posAt[idx], to: posAt[endIdx] };
      return false;
    }
    return undefined;
  });
  return hit;
}

// TipTap extension that paints the local tense-shift detector (lib/tense/detect)
// as inline squiggles in the editor, following the same decoration-plugin idiom
// as lib/Autocomplete.ts.
//
// Analysis is the expensive part (a POS pass per paragraph), so it runs on a
// debounce off the editing path: between recomputes the existing decorations are
// mapped through transactions so they track edits, then a fresh pass replaces
// them. Results are also mirrored into storage + an onShifts callback so a
// native/sidebar list can render the same findings.

import { Extension } from '@tiptap/core';
import { Plugin, PluginKey } from '@tiptap/pm/state';
import { Decoration, DecorationSet } from '@tiptap/pm/view';
import type { EditorView } from '@tiptap/pm/view';
import type { Node as PMNode } from '@tiptap/pm/model';
import { analyzeParagraph, loadTenseEngine, type ParagraphAnalysis, type Tense } from './tense/detect';
import { buildPosMap } from './proseMirrorText';

export interface TenseShiftHit {
  from: number;
  to: number;
  tense: Tense;
  expected: Tense;
  text: string;
}

export interface TenseShiftOptions {
  enabled: boolean;
  debounceMs: number;
  /** Skip paragraphs shorter than this many characters (too little to judge). */
  minChars: number;
  /** Called with the full set of hits after each recompute. */
  onShifts?: (hits: TenseShiftHit[]) => void;
}

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    tenseShift: {
      /** Turn the tense-shift checker on/off and recompute immediately. */
      setTenseCheck: (enabled: boolean) => ReturnType;
    };
  }
}

const tenseShiftKey = new PluginKey<DecorationSet>('tenseShift');

// Paragraph text rarely changes between recomputes, and the POS pass dominates
// cost, so memoize analysis by exact text. Bounded to avoid unbounded growth.
const analysisCache = new Map<string, ParagraphAnalysis>();
function analyze(text: string): ParagraphAnalysis {
  const cached = analysisCache.get(text);
  if (cached) return cached;
  const result = analyzeParagraph(text);
  // Evict oldest entries (Map preserves insertion order) instead of wiping the
  // whole cache — a full clear() past 500 paragraphs re-ran the POS pass on the
  // entire document on the next keystroke, a hard performance cliff on long books.
  while (analysisCache.size >= 500) {
    const oldest = analysisCache.keys().next().value;
    if (oldest === undefined) break;
    analysisCache.delete(oldest);
  }
  analysisCache.set(text, result);
  return result;
}

function yieldToMain(): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, 0));
}

// The POS tagger dominates cost and only runs on cache misses (see `analyze`
// above), so steady-state typing — where only the just-edited paragraph is
// uncached — finishes in a single slice with no added latency. On first
// enable or a chapter switch, every paragraph misses at once; time-slicing
// keeps that cold pass off the main thread in one uninterruptible block by
// yielding every ~8ms, and bails out entirely (returning null) if the doc
// changes mid-pass, mirroring Grammar's docBefore staleness check — a fresh
// pass is already scheduled for the new doc state.
const SLICE_MS = 8;

async function compute(
  view: EditorView,
  docBefore: PMNode,
  opts: TenseShiftOptions,
): Promise<{ decorations: DecorationSet; hits: TenseShiftHit[] } | null> {
  // Collecting paragraph text + position maps is a cheap doc walk (and
  // itself cached per-paragraph-node, see proseMirrorText.ts); only the
  // per-paragraph POS tagging below needs slicing.
  const paras: { text: string; posAt: number[] }[] = [];
  docBefore.descendants((node, pos) => {
    if (node.type.name !== 'paragraph') return;
    const { text, posAt } = buildPosMap(node, pos + 1);
    if (text.trim().length >= opts.minChars) paras.push({ text, posAt });
    return false; // don't descend into the paragraph's inline content
  });

  const decos: Decoration[] = [];
  const hits: TenseShiftHit[] = [];
  let batchStart = performance.now();

  for (let i = 0; i < paras.length; i++) {
    if (view.isDestroyed || view.state.doc !== docBefore) return null;

    const p = paras[i];
    const analysis = analyze(p.text);
    for (const sh of analysis.shifts) {
      const from = p.posAt[sh.start];
      const to = p.posAt[Math.min(sh.end, p.posAt.length - 1)];
      if (from == null || to == null || to <= from) continue;
      decos.push(
        Decoration.inline(
          from,
          to,
          { class: 'tense-shift' },
          { tense: sh.tense, expected: sh.expected },
        ),
      );
      hits.push({ from, to, tense: sh.tense, expected: sh.expected, text: sh.text.trim() });
    }

    if (performance.now() - batchStart >= SLICE_MS) {
      await yieldToMain();
      if (view.isDestroyed || view.state.doc !== docBefore) return null;
      batchStart = performance.now();
    }
  }

  return { decorations: DecorationSet.create(docBefore, decos), hits };
}

export const TenseShift = Extension.create<TenseShiftOptions>({
  name: 'tenseShift',

  addOptions() {
    return {
      enabled: false,
      debounceMs: 600,
      minChars: 12,
      onShifts: undefined,
    };
  },

  addStorage() {
    return {
      enabled: false,
      hits: [] as TenseShiftHit[],
    };
  },

  addCommands() {
    return {
      // Flip the flag and wake the plugin with a no-op transaction; the plugin's
      // view reacts to the enabled-change (lazy-loading the engine if needed).
      setTenseCheck:
        (enabled: boolean) =>
        ({ state, dispatch }) => {
          this.storage.enabled = enabled;
          if (dispatch) dispatch(state.tr);
          return true;
        },
    };
  },

  addProseMirrorPlugins() {
    const ext = this;
    // Seed storage from the configured default so the initial pass is gated.
    ext.storage.enabled = ext.options.enabled;
    let timer: ReturnType<typeof setTimeout> | null = null;

    return [
      new Plugin<DecorationSet>({
        key: tenseShiftKey,
        state: {
          init: () => DecorationSet.empty,
          apply(tr, old) {
            const meta = tr.getMeta(tenseShiftKey) as DecorationSet | undefined;
            if (meta) return meta;
            return old.map(tr.mapping, tr.doc);
          },
        },
        props: {
          decorations(state) {
            return tenseShiftKey.getState(state);
          },
        },
        view(view) {
          let prevEnabled = ext.storage.enabled;

          const schedule = () => {
            if (timer) clearTimeout(timer);
            timer = setTimeout(async () => {
              timer = null;
              if (!ext.storage.enabled) return;
              await loadTenseEngine(); // no-op after the first call
              if (!ext.storage.enabled || view.isDestroyed) return;
              const docBefore = view.state.doc;
              const result = await compute(view, docBefore, ext.options);
              // The document may have changed while we awaited a time slice; if
              // so, drop this stale result — a newer pass is already scheduled.
              if (!result || view.isDestroyed || view.state.doc !== docBefore) return;
              const { decorations, hits } = result;
              ext.storage.hits = hits;
              ext.options.onShifts?.(hits);
              view.dispatch(view.state.tr.setMeta(tenseShiftKey, decorations));
            }, ext.options.debounceMs);
          };

          const clear = () => {
            if (timer) clearTimeout(timer);
            timer = null;
            ext.storage.hits = [];
            ext.options.onShifts?.([]);
            view.dispatch(view.state.tr.setMeta(tenseShiftKey, DecorationSet.empty));
          };

          if (ext.storage.enabled) schedule();
          return {
            update(updatedView, prevState) {
              const enabledChanged = ext.storage.enabled !== prevEnabled;
              prevEnabled = ext.storage.enabled;
              if (ext.storage.enabled) {
                if (enabledChanged || !prevState.doc.eq(updatedView.state.doc)) schedule();
              } else if (enabledChanged) {
                clear();
              }
            },
            destroy() {
              if (timer) clearTimeout(timer);
            },
          };
        },
      }),
    ];
  },
});

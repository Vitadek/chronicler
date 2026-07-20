// TipTap extension that paints LanguageTool's grammar/style lints as inline
// squiggles, following the same debounced async-lint idiom as lib/TenseShift.ts.
//
// Linting is async (it crosses the network to the server's LanguageTool proxy),
// so the work runs off a debounce and the result is only applied if the document
// hasn't changed underneath it. Errors (spelling/grammar/typographical) get a
// red squiggle; stylistic/advisory notes get blue.

import { Extension } from '@tiptap/core';
import { Plugin, PluginKey } from '@tiptap/pm/state';
import { Decoration, DecorationSet } from '@tiptap/pm/view';
import type { EditorState } from '@tiptap/pm/state';
import { buildPosMap } from './proseMirrorText';
import { lintTextOrNull, loadGrammarEngine, type GrammarHit } from './grammar/languagetool';
import { getDictionary } from './dictionary';

export interface GrammarMark {
  from: number;
  to: number;
  kind: string;
  message: string;
  text: string;
  /** Dictionary correction candidates (misspellings only). */
  replacements?: string[];
}

export interface GrammarOptions {
  enabled: boolean;
  debounceMs: number;
  /** Skip paragraphs shorter than this many characters. */
  minChars: number;
  /**
   * Called with the full set of marks after each recompute ('lint'), and with
   * an empty set when the checker is switched off ('cleared'). Consumers that
   * show "checking…" states must not treat 'cleared' as a finished lint.
   */
  onMarks?: (marks: GrammarMark[], reason?: 'lint' | 'cleared') => void;
}

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    grammar: {
      /** Turn the grammar checker on/off and recompute (lazy-loads the engine). */
      setGrammarCheck: (enabled: boolean) => ReturnType;
    };
  }
}

const grammarKey = new PluginKey<DecorationSet>('grammar');

// Outright errors read red; stylistic/advisory notes read blue. LanguageTool
// issue types: misspelling | grammar | typographical | style | uncategorized | …
const ERROR_KINDS = new Set(['misspelling', 'grammar', 'typographical', 'whitespace']);
function classFor(kind: string): string {
  return ERROR_KINDS.has(kind) ? 'grammar-lint grammar-error' : 'grammar-lint grammar-style';
}

// Paragraph text rarely changes between recomputes and a lint round-trips a
// worker, so memoize by exact text. Bounded to avoid unbounded growth.
//
// A failed request (LanguageTool sidecar down/cold, offline, timeout) must
// never be cached as "no issues" — that would permanently suppress lints for
// the paragraph for the rest of the session. lintTextOrNull returns null on
// failure, and only successful results are written into lintCache; a failed
// paragraph is retried on the next debounced pass.
const lintCache = new Map<string, GrammarHit[]>();

// Dedup overlapping passes: if a debounce refires while a paragraph's lint is
// still in flight, await the same promise instead of re-POSTing it.
const pendingLints = new Map<string, Promise<GrammarHit[]>>();

async function lintCached(text: string): Promise<GrammarHit[]> {
  const cached = lintCache.get(text);
  if (cached) return cached;
  const pending = pendingLints.get(text);
  if (pending) return pending;

  const promise = (async () => {
    const hits = await lintTextOrNull(text);
    if (hits !== null) {
      // Evict oldest entries (Map preserves insertion order) instead of wiping
      // the whole cache — a full clear() past 500 paragraphs re-linted the
      // entire document on the next keystroke, a hard performance cliff on
      // long manuscripts.
      while (lintCache.size >= 500) {
        const oldest = lintCache.keys().next().value;
        if (oldest === undefined) break;
        lintCache.delete(oldest);
      }
      lintCache.set(text, hits);
    }
    return hits ?? [];
  })();
  pendingLints.set(text, promise);
  try {
    return await promise;
  } finally {
    pendingLints.delete(text);
  }
}

async function compute(
  state: EditorState,
  opts: GrammarOptions,
  isCurrentGen: () => boolean,
): Promise<{ decorations: DecorationSet; marks: GrammarMark[] }> {
  // Collect paragraph text + position maps up front (sync), then lint (async).
  const paras: { text: string; posAt: number[] }[] = [];
  state.doc.descendants((node, pos) => {
    if (node.type.name !== 'paragraph') return;
    const { text, posAt } = buildPosMap(node, pos + 1);
    if (text.trim().length >= opts.minChars) paras.push({ text, posAt });
    return false;
  });

  const decos: Decoration[] = [];
  const marks: GrammarMark[] = [];
  // Words the user added to their custom dictionary (proper nouns,
  // worldbuilding terms) are never flagged as misspellings — here in the
  // normal editor and in the Proofread view alike.
  const dictionary = getDictionary();
  // Lint paragraphs with a small concurrency pool instead of one-at-a-time:
  // a long chapter is dozens of round-trips to LanguageTool, and running them
  // serially made a full-chapter check take ~10s. Order is preserved by
  // writing results into a fixed-size array.
  const CONCURRENCY = 4;
  const lintResults: GrammarHit[][] = new Array(paras.length);
  let nextIdx = 0;
  await Promise.all(
    Array.from({ length: Math.min(CONCURRENCY, paras.length) }, async () => {
      while (nextIdx < paras.length) {
        // A newer pass has already been scheduled (debounce refired while this
        // one was still awaiting the network) — stop issuing new requests.
        // Paragraphs already in flight still complete and populate lintCache/
        // pendingLints for whoever needs them; this result itself is discarded
        // by the caller's staleness check.
        if (!isCurrentGen()) return;
        const i = nextIdx++;
        lintResults[i] = await lintCached(paras[i].text);
      }
    }),
  );
  for (let i = 0; i < paras.length; i++) {
    const p = paras[i];
    const lints = lintResults[i] ?? [];
    for (const ln of lints) {
      const flagged = p.text.slice(ln.start, ln.end);
      if (ln.kind === 'misspelling' && dictionary.has(flagged.trim().toLowerCase())) continue;
      const from = p.posAt[ln.start];
      const to = p.posAt[Math.min(ln.end, p.posAt.length - 1)];
      if (from == null || to == null || to <= from) continue;
      decos.push(
        Decoration.inline(from, to, { class: classFor(ln.kind), title: ln.message }, { kind: ln.kind }),
      );
      marks.push({ from, to, kind: ln.kind, message: ln.message, text: flagged, replacements: ln.replacements });
    }
  }

  return { decorations: DecorationSet.create(state.doc, decos), marks };
}

export const Grammar = Extension.create<GrammarOptions>({
  name: 'grammar',

  addOptions() {
    return {
      enabled: false,
      debounceMs: 800,
      minChars: 12,
      onMarks: undefined,
    };
  },

  addStorage() {
    return {
      enabled: false,
      marks: [] as GrammarMark[],
    };
  },

  addCommands() {
    return {
      setGrammarCheck:
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
    ext.storage.enabled = ext.options.enabled;
    let timer: ReturnType<typeof setTimeout> | null = null;

    return [
      new Plugin<DecorationSet>({
        key: grammarKey,
        state: {
          init: () => DecorationSet.empty,
          apply(tr, old) {
            const meta = tr.getMeta(grammarKey) as DecorationSet | undefined;
            if (meta) return meta;
            return old.map(tr.mapping, tr.doc);
          },
        },
        props: {
          decorations(state) {
            return grammarKey.getState(state);
          },
        },
        view(view) {
          let prevEnabled = ext.storage.enabled;
          // Incremented each time a pass actually starts (not merely
          // scheduled); a pass whose generation no longer matches the latest
          // one has been superseded by a newer debounce firing while it was
          // still awaiting the network, and stops issuing new requests.
          let currentGen = 0;

          const schedule = () => {
            if (timer) clearTimeout(timer);
            timer = setTimeout(async () => {
              timer = null;
              if (!ext.storage.enabled) return;
              await loadGrammarEngine(); // no-op after first call
              if (!ext.storage.enabled || view.isDestroyed) return;
              const gen = ++currentGen;
              const docBefore = view.state.doc;
              const { decorations, marks } = await compute(view.state, ext.options, () => gen === currentGen);
              // Drop this result if a newer pass superseded it, or if the
              // document changed while we awaited the worker — a newer pass
              // is already scheduled in either case.
              if (gen !== currentGen || view.isDestroyed || view.state.doc !== docBefore) return;
              ext.storage.marks = marks;
              ext.options.onMarks?.(marks, 'lint');
              view.dispatch(view.state.tr.setMeta(grammarKey, decorations));
            }, ext.options.debounceMs);
          };

          const clear = () => {
            if (timer) clearTimeout(timer);
            timer = null;
            ext.storage.marks = [];
            ext.options.onMarks?.([], 'cleared');
            view.dispatch(view.state.tr.setMeta(grammarKey, DecorationSet.empty));
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

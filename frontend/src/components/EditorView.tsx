import React, { lazy, Suspense, useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { EditorContent, ReactRenderer } from '@tiptap/react';
import { useChronicleEditor, UseChronicleEditorProps } from '../hooks/useChronicleEditor';
import { FormattingToolbar } from './FormattingToolbar';
import { CommandPortal } from './CommandPortal';
import { cn } from '../lib/utils';
import { authService } from '../services/authService';
import { motion, AnimatePresence } from 'motion/react';
import { MessageSquare, Check, X } from 'lucide-react';
import { loadCoverBlobUrl } from '../services/coverService';
import { usePluginHost, usePluginSlot } from '../plugins/host/PluginHost';
import { CompanionHost } from '../plugins/host/CompanionHost';
import { SelectionActionsHost } from '../plugins/host/SelectionActionsHost';
import tippy from 'tippy.js';
import 'tippy.js/dist/tippy.css';
import 'tippy.js/animations/shift-away.css';

// Yjs + Hocuspocus are only downloaded for the explicit collaboration opt-in.
const CollabEditor = lazy(() => import('./CollabEditor').then((m) => ({ default: m.CollabEditor })));

// A16 (frontend_optimizations.md): toLocaleTimeString with an options bag
// constructs a fresh Intl.DateTimeFormat per call; this renders once per
// keystroke (the last-write tracker below), so hoist a single cached
// formatter instead.
const LAST_WRITE_TIME_FMT = new Intl.DateTimeFormat([], {
  hour12: false,
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
});

interface EditorViewProps {
  isDarkMode: boolean;
  onToggleTheme: () => void;
  manuscriptId: string;
  /** Current chapter id, used to key the collaborative Y.Doc. */
  chapterId?: string;
  isTitlePage?: boolean;
  /** Cover image filename to display at the top of the title page. */
  coverArt?: string;
  isAutocompleteEnabled?: boolean;
  /** Live grammar/style squiggles in the body editor (lib/Grammar.ts, LanguageTool). */
  isGrammarCheckEnabled?: boolean;
  /** Deterministic autocorrect + sentence-start capitalization (lib/AutoCorrect.ts). */
  isAutoCorrectEnabled?: boolean;
  onGrammarMarks?: (marks: import('../lib/Grammar').GrammarMark[]) => void;
  isZenModeEnabled?: boolean;
  /** Whether body paragraphs render with a first-line indent. SMF default. */
  isFirstLineIndentEnabled?: boolean;
  /** Touch UI: swap the floating selection bubble for a docked bottom bar. */
  isTouchUI?: boolean;
  isSidebarOpen: boolean;
  sceneBreakStyle: 'classic' | 'dots' | 'ornamental' | 'custom';
  customSceneBreakSvg?: string;
  lastModified: number;
  manuscriptFont: string;
  title: string;
  content: string;
  onUpdate: (title: string, content: string) => void;
  /** Hands the live TipTap editor up to the parent so the sidebar's
   *  Comments panel can read marks and apply edits in-place. */
  onEditorReady?: (editor: any) => void;
  className?: string;
}

export const EditorView: React.FC<EditorViewProps> = ({ 
  isDarkMode, 
  onToggleTheme, 
  manuscriptId,
  chapterId,
  isTitlePage,
  coverArt,
  isAutocompleteEnabled,
  isGrammarCheckEnabled = false,
  isAutoCorrectEnabled = true,
  onGrammarMarks,
  isZenModeEnabled,
  isFirstLineIndentEnabled = true,
  isTouchUI = false,
  isSidebarOpen,
  sceneBreakStyle,
  customSceneBreakSvg,
  lastModified,
  manuscriptFont,
  title,
  content, 
  onUpdate,
  onEditorReady,
  className
}) => {
  // Opt-in real-time collaboration (Phase 2): set localStorage chronicle_collab
  // to '1' and reload. Connects to the server's /collab endpoint and keys the
  // shared Y.Doc by manuscript + chapter. Title page is excluded for now.
  // Hoisted above the hooks below (rather than left near the collab JSX
  // further down) so onEditorReady wiring can gate on it — see G1 in
  // frontend_optimizations.md: in collab mode, activeEditor must point at
  // CollabEditor's editor, not the detached hidden non-collab one.
  const collabEnabled =
    typeof window !== 'undefined' &&
    localStorage.getItem('chronicle_collab') === '1' &&
    !!authService.userId &&
    !isTitlePage;
  const collabUrl =
    typeof window !== 'undefined'
      ? `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/collab`
      : '';
  const collabDocName = `${encodeURIComponent(authService.userId ?? '')}/${manuscriptId}:${chapterId || 'content'}`;
  // Bearer token (oidc/token mode) so the secured collab socket authenticates.
  const collabToken =
    typeof window !== 'undefined' ? localStorage.getItem('chronicle_token') ?? undefined : undefined;

  const [isZenTriggered, setIsZenTriggered] = useState(false);
  const [isManualZen, setIsManualZen] = useState(false);
  // A9/A13 (frontend_optimizations.md): activity used to be a useState,
  // written on every keystroke AND every capture-phase window
  // mousedown/touchstart/scroll (i.e. at scroll frequency) — each write
  // re-rendered this whole subtree even when zen mode is off, since only the
  // 1s interval effect below ever reads it. A ref is enough: nothing needs a
  // render just because the clock moved.
  const lastActivityRef = useRef(Date.now());
  const [wordCountAtWake, setWordCountAtWake] = useState(0);
  const [commentingAt, setCommentingAt] = useState<{ from: number; to: number; text: string } | null>(null);
  const [commentDraft, setCommentDraft] = useState('');

  const { makeContext, reportError } = usePluginHost();
  const pluginSlashCommands = usePluginSlot('slashCommands');
  const pluginSelectionActions = usePluginSlot('selectionActions');

  // TipTap extensions contributed by enabled plugins. Memoized on the loaded-plugin set so the editor is
  // rebuilt only when plugins are enabled/disabled — not on every render.
  const editorExtensionSlots = usePluginSlot('editorExtensions');
  const pluginExtensions = useMemo(
    () =>
      editorExtensionSlots.flatMap(({ pluginId, item }) => {
        try {
          return item(makeContext(pluginId));
        } catch (err) {
          reportError(pluginId, err instanceof Error ? err.message : String(err));
          return [];
        }
      }),
    [editorExtensionSlots, makeContext, reportError],
  );

  // Listen for double-clicks on comment markers (dispatched from src/lib/Comment.ts)
  useEffect(() => {
    const handleEditComment = (e: any) => {
      const { from, to, comment, text } = e.detail;
      setCommentingAt({ from, to, text });
      setCommentDraft(comment || '');
    };
    window.addEventListener('edit-comment', handleEditComment);
    return () => window.removeEventListener('edit-comment', handleEditComment);
  }, []);

  // Title-page cover art is auth-gated, so resolve it to a blob URL rather
  // than putting the stored filename directly into an image source.
  const [titleCoverUrl, setTitleCoverUrl] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    if (!isTitlePage || !coverArt) {
      setTitleCoverUrl(null);
      return;
    }
    void loadCoverBlobUrl(coverArt).then((url) => {
      if (!cancelled) setTitleCoverUrl(url);
    });
    return () => { cancelled = true; };
  }, [isTitlePage, coverArt]);

  // The CommandPortal closures are created once when the editor mounts and
  // captured inside the CommandLine extension (the useMemo(..., []) below).
  // Plugin commands need the current context even though TipTap extensions
  // are created once for the editor's lifetime.
  const liveRef = useRef({
    makeContext,
    reportError,
    pluginSlashCommands,
  });
  useEffect(() => {
    liveRef.current = {
      makeContext,
      reportError,
      pluginSlashCommands,
    };
  });

  const commandLineOptions = useMemo(() => ({
    render: () => {
      let component: any;
      let popup: any;

      // Single dispatch handler shared by onStart and onUpdate — these
      // previously carried two hand-synced copies of this whole block.
      const makeCommand = (props: any) => async ({ command, args }: { command: string, args: string[] }) => {
        const { editor, range } = props;
        const live = liveRef.current;

        // Plugin-contributed commands win over the built-ins.
        const match = live.pluginSlashCommands.find((c) => c.item.name === command);
        if (match) {
          popup[0].hide();
          editor.commands.deleteRange(range);
          try {
            await match.item.run(live.makeContext(match.pluginId), args);
          } catch (err) {
            // A throwing command disables nothing, but it must not take the
            // editor down with it.
            live.reportError(match.pluginId, err instanceof Error ? err.message : String(err));
          }
          return;
        }

        if (command === 'comment') {
          // Hide popup immediately
          popup[0].hide();

          // Get paragraph content before clearing the command
          const { $from } = editor.state.selection;
          // We extract the current selection's parent text
          const rawText = $from.parent.textContent;
          // Clean #!/comment or #!command from the reference text
          const parentContent = rawText.replace(/#!(\/)?comment/gi, '').trim();

          // The range for the comment is the whole parent block minus the command
          const from = $from.start();
          const to = $from.end();

          // Clean up the command characters in the editor
          editor.commands.deleteRange(range);

          // Open commentary UI
          setCommentingAt({ from, to, text: parentContent || "this paragraph" });
          setCommentDraft('');
        } else if (command === 'epigraph') {
          popup[0].hide();
          editor.chain()
            .focus()
            .deleteRange(range)
            .setEpigraph()
            .run();
        }
      };

      return {
        onStart: (props: any) => {
          component = new ReactRenderer(CommandPortal, {
            props: {
              ...props,
              command: makeCommand(props),
            },
            editor: props.editor,
          });

          popup = tippy('body', {
            getReferenceClientRect: props.clientRect,
            appendTo: () => document.body,
            content: component.element,
            showOnCreate: true,
            interactive: true,
            trigger: 'manual',
            placement: 'bottom-start',
            zIndex: 100,
          });
        },
        onUpdate(props: any) {
          component.updateProps({
            ...props,
            command: makeCommand(props),
          });
          popup[0].setProps({
            getReferenceClientRect: props.clientRect,
          });
        },
        onKeyDown(props: any) {
          if (props.event.key === 'Escape') {
            popup[0].hide();
            return true;
          }
          return component.ref?.onKeyDown(props);
        },
        onExit() {
          popup[0].destroy();
          component.destroy();
        },
      };
    },
  }), []);

  // A1 (frontend_optimizations.md): the body editor's onUpdate used to call
  // onUpdate(title, html) synchronously on every keystroke, round-tripping the
  // whole chapter through App's ~40-useState root and re-rendering the entire
  // tree (Sidebar's DOMParser pass, a second getHTML() in the sync effect
  // below) once per character. Emission is now debounced; the live DOM is
  // always the source of truth (the editor itself never lags), only the
  // React/App-state mirror — and therefore autosave — trails by up to
  // BODY_EMIT_DEBOUNCE_MS. flushBodyEmit is load-bearing: it must run before
  // this component's content could be attributed to a different chapter, so
  // it fires on blur and (via the empty-deps cleanup below) on unmount —
  // chapter switches remount EditorView entirely (App keys it by
  // currentChapterId), so unmount-flush also covers switches.
  const BODY_EMIT_DEBOUNCE_MS = 280;
  const bodyOnUpdateRef = useRef(onUpdate);
  bodyOnUpdateRef.current = onUpdate;
  const titleRef = useRef(title);
  titleRef.current = title;
  const isTitlePageRef = useRef(isTitlePage);
  isTitlePageRef.current = isTitlePage;
  // What this component last emitted (or is about to emit) to App, in the
  // exact shape the `content` prop will echo back as — lets the sync effect
  // recognize its own echo without a second getHTML() call.
  const lastEmittedContentRef = useRef(content);
  const pendingBodyHtmlRef = useRef<string | null>(null);
  const bodyEmitTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const flushBodyEmit = useCallback(() => {
    if (bodyEmitTimerRef.current) {
      clearTimeout(bodyEmitTimerRef.current);
      bodyEmitTimerRef.current = null;
    }
    if (pendingBodyHtmlRef.current !== null) {
      const html = pendingBodyHtmlRef.current;
      pendingBodyHtmlRef.current = null;
      bodyOnUpdateRef.current(
        titleRef.current,
        isTitlePageRef.current ? html.replace(/<[^>]*>?/gm, '').trim() : html,
      );
    }
  }, []);

  // flushBodyEmit's identity is stable (useCallback with no deps), so this
  // cleanup fires exactly once, on unmount.
  useEffect(() => () => flushBodyEmit(), [flushBodyEmit]);

  const titleEditor = useChronicleEditor({
    content: `<h1>${title}</h1>`,
    placeholder: isTitlePage ? 'Manuscript Title' : 'Chapter Title',
    className: cn('novel-title-editor focus:outline-none mb-12', isTitlePage && 'text-center text-4xl sm:text-6xl'),
    isAutocompleteEnabled,
    // Titles are deliberate (proper nouns, stylistic casing) — don't autocorrect them.
    isAutoCorrectEnabled: false,
    // The title is always exactly one H1: Ctrl+A + Delete empties it instead of
    // demoting it to a paragraph, and Enter can't split it into extra blocks.
    singleLineHeading: true,
    isTouchUI,
    onUpdate: (html) => {
      const text = html.replace(/<[^>]*>?/gm, '').trim();
      onUpdate(text, content);
      lastActivityRef.current = Date.now();
    }
  });

  const editor = useChronicleEditor({
    content: isTitlePage ? (content.includes('<p>') ? content : `<p>${content}</p>`) : content,
    placeholder: isTitlePage ? 'Author Name' : 'Once upon a time in...',
    className: cn('novel-editor-content focus:outline-none', !isTitlePage && 'min-h-[500px]', isTitlePage && 'text-center text-2xl'),
    // In collab mode this editor is built but never mounted (CollabEditor
    // renders instead, see below) — its only job is to stay ready in case
    // collab is toggled off. Skip the checker/autocomplete passes on this
    // hidden, content-frozen doc: they're pure waste (nothing reads a doc
    // that's never shown) and previously overwrote parent checker state
    // with results from stale, divergent content (G1 in
    // frontend_optimizations.md).
    isAutocompleteEnabled: isAutocompleteEnabled && !collabEnabled,
    isGrammarCheckEnabled: isGrammarCheckEnabled && !isTitlePage && !collabEnabled,
    isAutoCorrectEnabled: isAutoCorrectEnabled && !isTitlePage,
    onGrammarMarks,
    isTouchUI,
    commandLineOptions,
    // Plugin extensions apply to the prose, not the title field.
    pluginExtensions: isTitlePage ? undefined : pluginExtensions,
    onUpdate: (html) => {
      lastEmittedContentRef.current = isTitlePage ? html.replace(/<[^>]*>?/gm, '').trim() : html;
      pendingBodyHtmlRef.current = html;
      if (bodyEmitTimerRef.current) clearTimeout(bodyEmitTimerRef.current);
      bodyEmitTimerRef.current = setTimeout(() => {
        bodyEmitTimerRef.current = null;
        flushBodyEmit();
      }, BODY_EMIT_DEBOUNCE_MS);
      lastActivityRef.current = Date.now();
    }
  });

  // Flush on blur too (tab-switch, clicking into the title field, etc.) —
  // unmount-flush alone would leave a debounced edit stuck in memory if the
  // user navigates away without the chapter itself changing.
  useEffect(() => {
    if (!editor) return;
    editor.on('blur', flushBodyEmit);
    return () => { editor.off('blur', flushBodyEmit); };
  }, [editor, flushBodyEmit]);

  // Bubble the editor up to App so the sidebar's Comments panel can read
  // and edit its marks. Cleanup on unmount clears the ref so a defunct
  // editor doesn't linger. In collab mode this hidden editor is never what
  // the user types in — CollabEditor reports its own editor instead (see the
  // render below) so App's activeEditor targets the live doc, not a detached
  // one (G1 in frontend_optimizations.md).
  useEffect(() => {
    if (collabEnabled) return;
    onEditorReady?.(editor);
    return () => onEditorReady?.(null);
  }, [editor, onEditorReady, collabEnabled]);

  // A7 (frontend_optimizations.md): CharacterCount.words() is a full
  // node.textBetween() over the whole doc — O(chapter text) — and this line
  // ran on EVERY render unconditionally, even with zen mode off, purely to
  // feed the zen trigger below. Gated so non-zen users (and this app's
  // default) pay nothing.
  const words = isZenModeEnabled
    ? (titleEditor?.storage.characterCount?.words() || 0) + (editor?.storage.characterCount?.words() || 0)
    : 0;
  // A9/A13: read via ref inside the interval/wakeUp handlers below instead of
  // listing `words` in their effect deps, so those effects don't tear down
  // and re-register roughly once per word typed.
  const wordsRef = useRef(words);
  wordsRef.current = words;

  // Zenith Trigger Effect: Only trigger after 3 words have been typed
  useEffect(() => {
    if (!isZenModeEnabled) return;
    if (words - wordCountAtWake >= 3 && !isZenTriggered && words > 0) {
      setIsZenTriggered(true);
    }
  }, [isZenModeEnabled, words, wordCountAtWake, isZenTriggered]);

  // Bring back UI on interactions (Click/Tap). A9: gated on isZenModeEnabled
  // (every consumer of this listener — the interval effect below — is
  // zen-only, so non-zen users previously paid for three capture-phase
  // window listeners, including `scroll`, for nothing) and no longer calls
  // setState per event; lastActivityRef absorbs the write.
  useEffect(() => {
    if (!isZenModeEnabled) return;
    const wakeUp = (e: MouseEvent | TouchEvent | Event) => {
      // Ignore clicks if they are inside a Tippy popup or on a comment widget
      const target = e.target as HTMLElement;
      if (!target || typeof target.closest !== 'function') return;

      const isInsideTippy = target.closest('[data-tippy-root]');
      const isCommentWidget = target.closest('.comment-icon-widget');

      if (isInsideTippy || isCommentWidget) {
        return;
      }

      lastActivityRef.current = Date.now();
      if (isZenTriggered) {
        setTimeout(() => {
          setIsZenTriggered(false);
          setWordCountAtWake(wordsRef.current);
        }, 50);
      }
    };

    window.addEventListener('mousedown', wakeUp as any, true);
    window.addEventListener('touchstart', wakeUp as any, true);
    window.addEventListener('scroll', wakeUp as any, true);

    return () => {
      window.removeEventListener('mousedown', wakeUp as any, true);
      window.removeEventListener('touchstart', wakeUp as any, true);
      window.removeEventListener('scroll', wakeUp as any, true);
    };
  }, [isZenModeEnabled, isZenTriggered]);

  // Sync content on chapter switch.
  // The isDestroyed guard is load-bearing: TipTap v3 destroys editors
  // ASYNCHRONOUSLY on unmount/recreation and nulls the schema, so a destroyed
  // instance can still sit in state when this effect fires — getHTML() on it
  // throws (scripts/destroyedEditorGuard.test.ts pins that behavior). The new
  // editor mounts with the right content anyway, so skipping is correct.
  useEffect(() => {
    if (!editor || editor.isDestroyed) return;
    // A1/A4 (frontend_optimizations.md): content echoing back exactly what
    // this component last emitted is this editor's own edit, not an external
    // change — skip re-deriving it from the DOM entirely (no getHTML()/
    // getText() call) rather than doing the work and discovering they match.
    if (content === lastEmittedContentRef.current) return;
    if (isTitlePage) {
      const currentText = editor.getText().trim();
      if (currentText !== content.trim()) {
        editor.commands.setContent(`<p>${content}</p>`);
      }
    } else if (content !== editor.getHTML()) {
      editor.commands.setContent(content);
    }
  }, [content, editor, isTitlePage]);

  useEffect(() => {
    if (!titleEditor || titleEditor.isDestroyed) return;
    const currentText = titleEditor.getText().trim();
    const targetText = title.trim();

    if (currentText !== targetText) {
      titleEditor.commands.setContent(`<h1>${title}</h1>`);
    }
  }, [title, titleEditor, isTitlePage]);

  // A9/A13: gated on isZenModeEnabled (pointless for non-zen users) and reads
  // both the activity clock and word count via refs, so this no longer tears
  // down/re-creates the timer on every keystroke or word typed.
  useEffect(() => {
    if (!isZenModeEnabled) return;
    const timer = setInterval(() => {
      // If idle for 10s, wake up and reset word count to prevent immediate re-trigger
      if (Date.now() - lastActivityRef.current > 10000 && isZenTriggered) {
        setIsZenTriggered(false);
        setWordCountAtWake(wordsRef.current);
      }
    }, 1000);
    return () => clearInterval(timer);
  }, [isZenModeEnabled, isZenTriggered]);


  const isZenActive = isZenModeEnabled && isZenTriggered;

  useEffect(() => {
    if (isZenActive) {
      document.body.classList.add('zen-active');
    } else {
      document.body.classList.remove('zen-active');
    }
  }, [isZenActive]);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setIsManualZen(false);
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, []);

  return (
    <div className={cn(
      "flex flex-col items-center w-full min-h-screen transition-all duration-1000",
      `scene-break-${sceneBreakStyle}`,
      className
    )}>
      <style>
        {`
          :root {
            --scene-break-style: "${sceneBreakStyle}";
            --custom-scene-break-svg: url("${customSceneBreakSvg || ''}");
          }
        `}
      </style>
      {/* Meta Tag: Last Write Wins Tracker */}
      <div 
        className="fixed top-2 right-8 z-[60] text-[9px] font-mono pointer-events-none select-none opacity-[0.1] hover:opacity-100 transition-opacity flex flex-col items-end"
      >
        <span className="tracking-widest">TS_{lastModified}</span>
        <span className="opacity-50 uppercase">{LAST_WRITE_TIME_FMT.format(lastModified)}</span>
      </div>

      <div 
        className="w-full flex-1 transition-all duration-700 manuscript-content-box"
      >
        <div className={cn(
          "novel-editor w-full h-full max-w-5xl mx-auto px-6 sm:px-10 md:px-0",
          isTitlePage ? "py-64" : "py-32",
          isFirstLineIndentEnabled && "indent"
        )}>
          {/* Title page hero: shows the uploaded cover art above the
              title fields. Sized to feel like a book cover, with a soft
              shadow. Falls back to nothing when no cover is uploaded. */}
          {isTitlePage && titleCoverUrl && (
            <div className="mb-16 flex justify-center" data-outline="cover">
              <img
                src={titleCoverUrl}
                alt="Manuscript cover"
                className="rounded-2xl shadow-2xl max-h-[60vh] max-w-[18rem] object-contain"
              />
            </div>
          )}
          <div data-outline="title">
            <EditorContent 
              editor={titleEditor} 
              className="w-full"
            />
            <FormattingToolbar
                editor={titleEditor}
                isDarkMode={isDarkMode}
                pluginKey="titleFormatting"
                isTouchUI={isTouchUI}
            />
          </div>
          {isTitlePage && (
            <div className="my-12 opacity-20 font-serif italic text-xl text-center">by</div>
          )}
          <div data-outline="content">
            {collabEnabled ? (
              <Suspense fallback={<div className="min-h-[500px] opacity-40">Connecting collaborative editor…</div>}>
                <CollabEditor
                  docName={collabDocName}
                  collabUrl={collabUrl}
                  token={collabToken}
                  className="w-full"
                  onEditorReady={onEditorReady}
                />
              </Suspense>
            ) : (
              <EditorContent
                editor={editor}
                className="w-full"
              />
            )}
            {!collabEnabled && (
              <FormattingToolbar
                editor={editor}
                isDarkMode={isDarkMode}
                pluginKey="contentFormatting"
                isTouchUI={isTouchUI}
              />
            )}

            {/* Plugin Layer */}
            {!collabEnabled && editor && !isTitlePage && (
              <>
                <SelectionActionsHost editor={editor} isDarkMode={isDarkMode} />
                <CompanionHost />
              </>
            )}

            {/* Inline Comment Entry UI */}
            <AnimatePresence>
              {commentingAt && (
                <motion.div
                  initial={{ opacity: 0, scale: 0.98 }}
                  animate={{ opacity: 1, scale: 1 }}
                  exit={{ opacity: 0, scale: 0.98 }}
                  className="mt-8 p-6 rounded-2xl bg-[#1A1918] border border-white/10 shadow-[0_32px_64px_-16px_rgba(0,0,0,0.5)] z-[70] max-w-xl mx-auto overflow-hidden relative"
                >
                  {/* Decorative background glow */}
                  <div className="absolute top-0 left-0 w-32 h-32 bg-white/5 rounded-full blur-3xl -translate-x-1/2 -translate-y-1/2 pointer-events-none" />
                  
                  <div className="relative z-10">
                    <div className="flex items-center justify-between mb-6">
                      <div className="flex items-center gap-3">
                        <div className="p-2 rounded-lg bg-white/5 border border-white/10">
                          <MessageSquare className="w-4 h-4 text-[#F1EDE4]" />
                        </div>
                        <div>
                          <h4 className="text-[10px] uppercase tracking-[0.2em] font-bold text-[#F1EDE4] opacity-40">Prose Commentary</h4>
                          <p className="text-[9px] opacity-20 italic">Annotating current paragraph</p>
                        </div>
                      </div>
                      <button 
                        onClick={() => setCommentingAt(null)}
                        className="p-1.5 rounded-full hover:bg-white/5 transition-colors opacity-30 hover:opacity-100"
                      >
                        <X className="w-4 h-4" />
                      </button>
                    </div>

                    <textarea
                      autoFocus
                      value={commentDraft}
                      onChange={(e) => setCommentDraft(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' && e.ctrlKey) {
                          e.preventDefault();
                          const { from, to } = commentingAt;
                          editor?.chain()
                            .focus()
                            .setTextSelection({ from, to })
                            .setMark('comment', { comment: commentDraft })
                            .setTextSelection(to)
                            .run();
                          setCommentingAt(null);
                        }
                        if (e.key === 'Escape') {
                          setCommentingAt(null);
                        }
                      }}
                      placeholder="Capture your thoughts..."
                      className="w-full bg-white/[0.03] border border-white/15 rounded-xl p-4 text-sm text-[#F1EDE4] placeholder:opacity-20 focus:outline-none focus:border-white/20 min-h-[160px] mb-6 transition-all duration-300 font-serif resize-none leading-relaxed"
                    />

                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-4">
                        <div className="flex items-center gap-1.5 opacity-20">
                          <kbd className="px-1 py-0.5 rounded bg-white/10 text-[8px] font-mono border border-white/15">CTRL</kbd>
                          <span className="text-[8px] font-bold">+</span>
                          <kbd className="px-1 py-0.5 rounded bg-white/10 text-[8px] font-mono border border-white/15">ENTER</kbd>
                        </div>
                        <span className="text-[8px] opacity-20 uppercase tracking-widest font-bold">To confirm</span>
                      </div>

                      <div className="flex items-center gap-3">
                        <button 
                          onClick={() => {
                            const { from, to } = commentingAt;
                            editor?.chain()
                              .focus()
                              .setTextSelection({ from, to })
                              .unsetMark('comment')
                              .setTextSelection(to)
                              .run();
                            setCommentingAt(null);
                          }}
                          className="px-4 py-2 rounded-xl text-[10px] uppercase font-bold tracking-widest text-red-500/60 hover:text-red-500 transition-colors"
                        >
                          Remove
                        </button>
                        <button 
                          onClick={() => setCommentingAt(null)}
                          className="px-4 py-2 rounded-xl text-[10px] uppercase font-bold tracking-widest opacity-40 hover:opacity-100 transition-opacity"
                        >
                          Cancel
                        </button>
                        <button 
                          onClick={() => {
                            const { from, to } = commentingAt;
                            editor?.chain()
                              .focus()
                              .setTextSelection({ from, to })
                              .setMark('comment', { comment: commentDraft })
                              .setTextSelection(to)
                              .run();
                            setCommentingAt(null);
                          }}
                          className="flex items-center gap-2 px-6 py-2 rounded-xl bg-[#F1EDE4] text-black text-[10px] uppercase font-bold tracking-widest hover:bg-white hover:scale-[1.02] active:scale-[0.98] transition-all"
                        >
                          <Check className="w-3.5 h-3.5" />
                          Save Annotation
                        </button>
                      </div>
                    </div>
                  </div>
                </motion.div>
              )}
            </AnimatePresence>
          </div>
        </div>
      </div>

    </div>
  );
};

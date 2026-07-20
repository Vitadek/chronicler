import { lazy, Suspense, useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { LibraryView } from './components/LibraryView';
import { PluginHost, usePluginHost, usePublishPluginRuntime } from './plugins/host/PluginHost';
import { PluginViewHost } from './plugins/host/PluginViewHost';
import { ManuscriptServiceError, manuscriptService } from './services/manuscriptService';
import { startSync } from './services/syncService';
import { cn } from './lib/utils';
import { authService } from './services/authService';
import { LazyMotion, m } from 'motion/react';

// Lazy feature bundle for LazyMotion — keeps the ~130K motion runtime out of
// the eager entry chunk. domMax (not domAnimation) because LibraryView's
// manuscript cards use layoutId shared-layout projection.
const loadMotionFeatures = () => import('./lib/motionFeatures').then((mod) => mod.default);
import { Chapter, ManuscriptMetadata, UserProfile, Manuscript, ExportSettings, DEFAULT_EXPORT_SETTINGS } from './types';
import { countWords } from './lib/wordCount';
import { scheduleSettingsPush } from './lib/settingsSync';
import {
  clearManuscriptConflictDraft,
  clearManuscriptDraft,
  readManuscriptDraft,
} from './lib/manuscriptDraftJournal';
import {
  manuscriptFingerprint,
  useManuscriptAutosave,
  type ManuscriptSaveStatus,
} from './hooks/useManuscriptAutosave';
import type { Editor } from '@tiptap/react';

const Sidebar = lazy(() => import('./components/Sidebar').then((m) => ({ default: m.Sidebar })));
const EditorView = lazy(() => import('./components/EditorView').then((m) => ({ default: m.EditorView })));
const GlobalSettings = lazy(() => import('./components/GlobalSettings').then((m) => ({ default: m.GlobalSettings })));
const ProofreadView = lazy(() => import('./components/ProofreadView').then((m) => ({ default: m.ProofreadView })));

interface OpenSession {
  manuscriptId: string;
  key: number;
}

function RouteFallback({ isDarkMode, label = 'Loading…' }: { isDarkMode: boolean; label?: string }) {
  return (
    <div className={cn(
      'min-h-screen-dvh w-full flex items-center justify-center text-xs uppercase tracking-[0.2em]',
      isDarkMode ? 'bg-manuscript-dark text-white/40' : 'bg-manuscript-light text-black/40',
    )}>
      {label}
    </div>
  );
}

function SaveStatus({
  status,
  error,
  onRetry,
  canReloadServerVersion,
  hasConflictRecovery,
  onReloadServerVersion,
  onRestoreConflictDraft,
  onDiscardConflictDraft,
}: {
  status: ManuscriptSaveStatus;
  error: string | null;
  onRetry: () => void;
  canReloadServerVersion: boolean;
  hasConflictRecovery: boolean;
  onReloadServerVersion: () => void;
  onRestoreConflictDraft: () => void;
  onDiscardConflictDraft: () => void;
}) {
  const labels: Record<ManuscriptSaveStatus, string> = {
    saved: 'Saved',
    dirty: 'Unsaved changes',
    saving: 'Saving…',
    offline: 'Offline — draft kept locally',
    error: 'Save failed',
    conflict: 'Save conflict',
    'draft-error': 'Local draft unavailable',
  };
  const canRetry = status === 'offline' || status === 'error' || status === 'draft-error';
  const label = hasConflictRecovery && status === 'saved'
    ? 'Server loaded — local copy kept'
    : labels[status];

  return (
    <div
      aria-live="polite"
      title={error ?? label}
      className={cn(
        'fixed right-4 bottom-4 z-[80] flex items-center gap-2 rounded-full border px-3 py-2 text-[10px] font-bold uppercase tracking-widest shadow-lg backdrop-blur-md',
        status === 'error' || status === 'conflict' || status === 'draft-error'
          ? 'border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300'
          : 'border-black/10 bg-white/80 text-black/60 dark:border-white/10 dark:bg-black/70 dark:text-white/60',
      )}
    >
      <span className={cn(
        'h-1.5 w-1.5 rounded-full',
        status === 'saved' ? 'bg-emerald-500' :
          status === 'saving' ? 'bg-blue-500 animate-pulse' :
            status === 'offline' ? 'bg-amber-500' :
              status === 'error' || status === 'conflict' || status === 'draft-error'
                ? 'bg-red-500'
                : 'bg-black/30 dark:bg-white/30',
      )} />
      <span>{label}</span>
      {canRetry && (
        <button onClick={onRetry} className="underline underline-offset-2 hover:opacity-70">
          Retry
        </button>
      )}
      {status === 'conflict' && canReloadServerVersion && (
        <button onClick={onReloadServerVersion} className="underline underline-offset-2 hover:opacity-70">
          Reload server version
        </button>
      )}
      {hasConflictRecovery && (
        <>
          <button onClick={onRestoreConflictDraft} className="underline underline-offset-2 hover:opacity-70">
            Restore local edits
          </button>
          <button onClick={onDiscardConflictDraft} className="underline underline-offset-2 hover:opacity-70">
            Discard copy
          </button>
        </>
      )}
    </div>
  );
}

function AppInner() {
  const sessionSequenceRef = useRef(0);
  const openSessionRef = useRef<OpenSession | null>(null);
  // Keep the latest server acknowledgements outside React's render cycle so
  // a destructive action immediately following an awaited autosave uses the
  // revision returned by that save, not the revision captured by the click's
  // older render.
  const metadataRevisionRef = useRef<number | undefined>(undefined);
  const chapterRevisionsRef = useRef(new Map<string, number | undefined>());
  const [openSession, setOpenSession] = useState<OpenSession | null>(null);
  const currentManuscriptId = openSession?.manuscriptId ?? null;
  const [hydratedSessionKey, setHydratedSessionKey] = useState<number | null>(null);
  const [serverBaselineFingerprint, setServerBaselineFingerprint] = useState<string | null>(null);
  const [isSidebarOpen, setIsSidebarOpen] = useState(false);
  const [isDarkMode, setIsDarkMode] = useState(() => {
    return localStorage.getItem('chronicle_theme') === 'dark';
  });
  const [manuscriptFont, setManuscriptFont] = useState(() => {
    return localStorage.getItem('chronicle_manuscript_font') || 'Verdana';
  });
  const [isAutocompleteEnabled, setIsAutocompleteEnabled] = useState(() => {
    return localStorage.getItem('chronicle_autocomplete') !== 'false';
  });
  const [isGrammarCheckEnabled, setIsGrammarCheckEnabled] = useState(() => {
    return localStorage.getItem('chronicle_grammar_check') === 'true';
  });
  const [isAutoCorrectEnabled, setIsAutoCorrectEnabled] = useState(() => {
    return localStorage.getItem('chronicle_autocorrect') !== 'false';
  });
  // A11 (frontend_optimizations.md): checkers hand back a fresh array after
  // every debounced pass, even when the result is unchanged (e.g. still zero
  // findings) — bail out of the state update (and the two extra full-tree
  // re-renders it would cause) when the new set is the same length-zero set
  // as before. A full identity-preserving diff isn't needed for the common
  // "no findings either way" case this targets.
  const [isZenModeEnabled, setIsZenModeEnabled] = useState(() => {
    return localStorage.getItem('chronicle_zen_mode') === 'true';
  });
  const [isFirstLineIndentEnabled, setIsFirstLineIndentEnabled] = useState(() => {
    // Default ON — Standard Manuscript Format convention.
    return localStorage.getItem('chronicle_first_line_indent') !== 'false';
  });
  // Per-format export preferences (HTML theme, Hugo markdown front matter,
  // EPUB rights/cover). Merged over defaults so a stored partial from an older
  // build still yields a complete, well-shaped settings object.
  const [exportSettings, setExportSettings] = useState<ExportSettings>(() => {
    try {
      const raw = localStorage.getItem('chronicle_export_settings');
      if (!raw) return DEFAULT_EXPORT_SETTINGS;
      const parsed = JSON.parse(raw);
      return {
        html: { ...DEFAULT_EXPORT_SETTINGS.html, ...parsed.html },
        markdown: { ...DEFAULT_EXPORT_SETTINGS.markdown, ...parsed.markdown },
        epub: { ...DEFAULT_EXPORT_SETTINGS.epub, ...parsed.epub },
      };
    } catch {
      return DEFAULT_EXPORT_SETTINGS;
    }
  });

  /**
   * Touch-controls mode: 'auto' follows the device's pointer type, 'on'/'off'
   * are manual overrides for when detection is wrong (some Android browsers,
   * hybrid devices). Drives the `touch-ui` class on <html>, which all
   * touch-specific styling and the editor's selection toolbar key off.
   */
  const [touchControlsMode, setTouchControlsMode] = useState<'auto' | 'on' | 'off'>(() => {
    const v = localStorage.getItem('chronicle_touch_controls');
    return v === 'on' || v === 'off' ? v : 'auto';
  });
  const [coarsePointer, setCoarsePointer] = useState<boolean>(() =>
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia('(pointer: coarse)').matches
      : false,
  );
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return;
    const mql = window.matchMedia('(pointer: coarse)');
    const onChange = (e: MediaQueryListEvent) => setCoarsePointer(e.matches);
    // addEventListener is the modern API; older Safari used addListener.
    if (mql.addEventListener) mql.addEventListener('change', onChange);
    else mql.addListener(onChange);
    return () => {
      if (mql.removeEventListener) mql.removeEventListener('change', onChange);
      else mql.removeListener(onChange);
    };
  }, []);
  const isTouchUI =
    touchControlsMode === 'on' || (touchControlsMode === 'auto' && coarsePointer);
  useEffect(() => {
    localStorage.setItem('chronicle_touch_controls', touchControlsMode);
  }, [touchControlsMode]);
  useEffect(() => {
    document.documentElement.classList.toggle('touch-ui', isTouchUI);
  }, [isTouchUI]);

  const [userProfile, setUserProfile] = useState<UserProfile>(() => {
    const saved = localStorage.getItem('chronicle_user_profile');
    if (saved) return JSON.parse(saved);
    return { name: '', address: '', phone: '', email: '' };
  });

  const [metadata, setMetadata] = useState<ManuscriptMetadata | null>(null);
  const [chapters, setChapters] = useState<Chapter[]>([]);
  const [currentChapterId, setCurrentChapterId] = useState<string>('');

  // Live editor instance for the open chapter, published to plugin contexts.
  const [activeEditor, setActiveEditor] = useState<Editor | null>(null);

  // Global settings visibility
  const [isGlobalSettingsOpen, setIsGlobalSettingsOpen] = useState(false);

  // Proofread mode: the guided revision pass opened from a library card's
  // spell-check icon. When true, the open manuscript renders in ProofreadView
  // instead of the normal Sidebar+EditorView.
  const [isProofreadMode, setIsProofreadMode] = useState(false);

  const clearOpenDocument = useCallback(() => {
    metadataRevisionRef.current = undefined;
    chapterRevisionsRef.current.clear();
    setHydratedSessionKey(null);
    setServerBaselineFingerprint(null);
    setMetadata(null);
    setChapters([]);
    setCurrentChapterId('');
    setActiveEditor(null);
  }, []);

  /**
   * Start an immutable open session. Clearing the old document in the same
   * React batch is essential: an ID must never render with another book's
   * metadata while its GET is in flight.
   */
  const openManuscript = useCallback((manuscriptId: string, proofread = false) => {
    const next = { manuscriptId, key: ++sessionSequenceRef.current };
    openSessionRef.current = next;
    clearOpenDocument();
    setIsProofreadMode(proofread);
    setOpenSession(next);
  }, [clearOpenDocument]);

  const closeManuscriptImmediately = useCallback(() => {
    openSessionRef.current = null;
    setOpenSession(null);
    setIsProofreadMode(false);
    clearOpenDocument();
  }, [clearOpenDocument]);

  /**
   * Core features a plugin has taken over (manifest `replaces: ["core:grammar"]`).
   *
   * This SHADOWS: while the plugin is enabled, core simply stops rendering its
   * built-in — but the user's own `chronicle_grammar_check` setting is never
   * written. Flipping their toggle off would persist, and uninstalling the plugin
   * later would silently leave them with no grammar checking at all.
   *
   * The *_Active values below are what the app actually runs on. The raw
   * is*Enabled state still drives Global Settings, which greys the toggle out and
   * says which plugin owns it now.
   */
  const { shadowedCore } = usePluginHost();
  const coreOn = (capability: string) => !shadowedCore.has(capability);

  const grammarCheckActive = isGrammarCheckEnabled && coreOn('core:grammar');
  const autoCorrectActive = isAutoCorrectEnabled && coreOn('core:autocorrect');
  const proofreadActive = coreOn('core:proofreader');

  // Keep the plugin host's view of the app current. The host sits above this component so plugins survive
  // navigation; this is how it sees our live state.
  usePublishPluginRuntime({
    manuscriptId: currentManuscriptId,
    editor: activeEditor,
    onToast: (message) => {
      console.info('[plugin]', message);
    },
  });

  // Signal that bumps whenever a remote sync delivers fresh data. The
  // LibraryView watches this to reload its manuscript list, and the editor
  // re-fetches the current manuscript if it's affected.
  const [remoteRevision, setRemoteRevision] = useState(0);

  // Start the background sync loop on mount. Pull every 30s plus on focus
  // and network reconnect. When something changes, bump remoteRevision so
  // any open view refreshes itself from the (now-updated) server state.
  useEffect(() => {
    const stop = startSync({
      onPull: (resp) => {
        const touched =
          resp.pull.manuscripts.length +
          resp.pull.chapters.length +
          (resp.pull.profile ? 1 : 0);
        // A cursor reset means the server was restored. Refresh even when the
        // authoritative full-state replay is empty so stale library state is
        // not left visible.
        if (touched > 0 || resp.reset) setRemoteRevision((n) => n + 1);
      },
    });
    return stop;
  }, []);

  /**
   * Manuscript-wide word count, recomputed when chapter content changes.
   * Same numbers the sidebar shows in its totals row, so the Library view's
   * "X Words" badge stays in sync with what authors see while editing.
   *
   * Folded into manuscriptForSave below instead of a separate setMetadata
   * effect (A5 in frontend_optimizations.md): that effect re-rendered all of
   * AppInner plus both heavy lazy children (Sidebar's DOMParser pass,
   * EditorView) a SECOND time on every word boundary. The server copy — all
   * the Library view reads — still gets the fresh count on every save; only
   * the extra render-feedback loop through root state is gone.
   */
  const chapterWordCountCacheRef = useRef(new Map<string, { content: string; words: number }>());
  const totalWordCount = useMemo(() => {
    const cache = chapterWordCountCacheRef.current;
    const liveIds = new Set<string>();
    let total = 0;
    for (const chapter of chapters) {
      liveIds.add(chapter.id);
      const cached = cache.get(chapter.id);
      if (cached?.content === chapter.content) {
        total += cached.words;
      } else {
        const words = countWords(chapter.content);
        cache.set(chapter.id, { content: chapter.content, words });
        total += words;
      }
    }
    for (const id of cache.keys()) {
      if (!liveIds.has(id)) cache.delete(id);
    }
    return total;
  }, [chapters]);

  const manuscriptForSave = useMemo<Manuscript | null>(() => {
    if (
      !openSession ||
      hydratedSessionKey !== openSession.key ||
      !metadata ||
      chapters.length === 0
    ) {
      return null;
    }
    return {
      metadata: metadata.wordCount === totalWordCount ? metadata : { ...metadata, wordCount: totalWordCount },
      chapters,
    };
  }, [chapters, hydratedSessionKey, metadata, openSession, totalWordCount]);

  const handleSaveAcknowledged = useCallback((saved: Manuscript, sessionKey: number) => {
    if (openSessionRef.current?.key !== sessionKey) return;
    metadataRevisionRef.current = saved.metadata?.revision;
    chapterRevisionsRef.current = new Map(
      saved.chapters.map((chapter) => [chapter.id, chapter.revision]),
    );
    // Merge only server concurrency tokens. Replacing content here could
    // clobber editor transactions made while the PUT was in flight.
    setMetadata((prev) => {
      const revision = saved.metadata?.revision;
      return prev && revision !== undefined && prev.revision !== revision
        ? { ...prev, revision }
        : prev;
    });
    if (saved.chapters) {
      const revisions = new Map(saved.chapters.map((chapter) => [chapter.id, chapter.revision]));
      setChapters((prev) => prev.map((chapter) => {
        const revision = revisions.get(chapter.id);
        return revision !== undefined && revision !== chapter.revision
          ? { ...chapter, revision }
          : chapter;
      }));
    }
  }, []);

  const replaceOpenManuscript = useCallback((
    replacement: Manuscript,
    sessionKey: number,
    authoritative: boolean,
  ) => {
    if (openSessionRef.current?.key !== sessionKey) return;
    metadataRevisionRef.current = replacement.metadata.revision;
    chapterRevisionsRef.current = new Map(
      replacement.chapters.map((chapter) => [chapter.id, chapter.revision]),
    );
    if (authoritative) setServerBaselineFingerprint(manuscriptFingerprint(replacement));
    setMetadata(replacement.metadata);
    setChapters(replacement.chapters);
    setCurrentChapterId((current) =>
      current === 'title-page' || replacement.chapters.some((chapter) => chapter.id === current)
        ? current
        : replacement.chapters[0]?.id ?? 'title-page'
    );
  }, []);

  // Collab mode (opt-in localStorage flag — see EditorView.tsx) hands the
  // currently-open chapter's content to the server's Hocuspocus snapshot
  // exclusively; App's `chapters` state for it goes stale the moment the user
  // starts typing (CollabEditor deliberately has no onUpdate back to App, see
  // its file comment). Excluding that one chapter's content+revision from the
  // autosave PUT (below) stops every unrelated edit — a chapter title, a
  // reorder, metadata — from 409-conflicting against the server's own
  // collab-driven revision bumps and latching the whole manuscript's autosave
  // (G2 in frontend_optimizations.md). The server never deletes chapters
  // missing from a PUT's array (server/routes/manuscripts.ts), so omitting it
  // is safe — it's simply left untouched, which is correct: the Hocuspocus
  // snapshot is already the sole persister for it.
  const collabActiveChapterId =
    typeof window !== 'undefined' &&
    localStorage.getItem('chronicle_collab') === '1' &&
    !!authService.userId &&
    currentChapterId !== 'title-page'
      ? currentChapterId
      : null;

  const autosave = useManuscriptAutosave({
    sessionKey: manuscriptForSave ? hydratedSessionKey : null,
    manuscriptId: manuscriptForSave ? currentManuscriptId : null,
    manuscript: manuscriptForSave,
    baselineFingerprint: serverBaselineFingerprint,
    collabActiveChapterId,
    onSaved: handleSaveAcknowledged,
    onConflictReloaded: (authoritative, sessionKey) => {
      replaceOpenManuscript(authoritative, sessionKey, true);
    },
    onConflictDraftRestored: (draft, sessionKey) => {
      replaceOpenManuscript(draft, sessionKey, false);
    },
  });

  const reloadServerVersion = useCallback(() => {
    const confirmed = window.confirm(
      'Reload the authoritative server version? Chronicle will first keep your current local edits as a separate recovery copy, and will not overwrite the server with them.',
    );
    if (confirmed) autosave.reloadServerVersion();
  }, [autosave.reloadServerVersion]);

  const restoreConflictDraft = useCallback(() => {
    const confirmed = window.confirm(
      'Restore the preserved local edits into the editor? This replaces the currently displayed server text and resumes autosave using the latest server revisions.',
    );
    if (confirmed) autosave.restoreConflictDraft();
  }, [autosave.restoreConflictDraft]);

  const discardConflictDraft = useCallback(() => {
    const confirmed = window.confirm(
      'Permanently discard the preserved local conflict copy? The server version will remain unchanged.',
    );
    if (confirmed) autosave.discardConflictDraft();
  }, [autosave.discardConflictDraft]);

  const leaveManuscript = useCallback(async () => {
    const saved = await autosave.flush();
    if (!saved) {
      const recoveryConfirmed = autosave.hasRecoveryDraft();
      const leaveAnyway = window.confirm(
        recoveryConfirmed
          ? 'Chronicle could not sync your latest changes. The exact current version is preserved in the local draft journal. Leave this manuscript anyway?'
          : 'Chronicle could not sync your latest changes and could not confirm a recovery copy. Leaving now may lose them. Leave this manuscript anyway?',
      );
      if (!leaveAnyway) return;
    }
    closeManuscriptImmediately();
  }, [autosave.flush, autosave.hasRecoveryDraft, closeManuscriptImmediately]);

  // Load manuscript from server
  useEffect(() => {
    if (!openSession) return;

    const { manuscriptId, key: sessionKey } = openSession;
    const controller = new AbortController();

    const load = async () => {
      try {
        const serverManuscript = await manuscriptService.get(manuscriptId, controller.signal);
        if (controller.signal.aborted || openSessionRef.current?.key !== sessionKey) return;

        const baseline = manuscriptFingerprint(serverManuscript);
        let manuscript = serverManuscript;
        const draft = readManuscriptDraft(manuscriptId);
        if (draft) {
          const draftFingerprint = manuscriptFingerprint(draft.manuscript);
          if (draftFingerprint === baseline) {
            clearManuscriptDraft(manuscriptId);
          } else if (window.confirm(
            'Chronicle found changes from an interrupted or failed save. Restore the local draft?',
          )) {
            manuscript = draft.manuscript;
          } else {
            clearManuscriptDraft(manuscriptId);
          }
        }

        if (controller.signal.aborted || openSessionRef.current?.key !== sessionKey) return;
        setServerBaselineFingerprint(baseline);
        metadataRevisionRef.current = manuscript.metadata.revision;
        chapterRevisionsRef.current = new Map(
          manuscript.chapters.map((chapter) => [chapter.id, chapter.revision]),
        );
        setMetadata(manuscript.metadata);
        setChapters(manuscript.chapters);
        setCurrentChapterId(manuscript.chapters[0]?.id || 'title-page');
        setHydratedSessionKey(sessionKey);
      } catch (error) {
        if (controller.signal.aborted || (error instanceof DOMException && error.name === 'AbortError')) return;
        if (openSessionRef.current?.key !== sessionKey) return;
        console.error(error);
        alert('Failed to load manuscript');
        closeManuscriptImmediately();
      }
    };
    void load();

    // NOTE: deliberately not depending on remoteRevision. Reloading the
    // currently-open manuscript on every sync tick would clobber in-progress
    // edits the user hasn't auto-saved yet. The library view does react to
    // remoteRevision so adds/deletes from other devices show up there.
    return () => controller.abort();
  }, [closeManuscriptImmediately, openSession]);

  // Prefill contact name from author if empty
  useEffect(() => {
    if (metadata && !metadata.contactName && metadata.author && metadata.author !== 'Uncredited Author') {
      setMetadata(prev => prev ? ({ ...prev, contactName: prev.author }) : null);
    }
  }, [metadata?.author]);

  // Sync profile name to metadata author if needed
  useEffect(() => {
    if (metadata?.author === 'Uncredited Author' && userProfile.name) {
      setMetadata(prev => prev ? ({ ...prev, author: userProfile.name }) : null);
    }
  }, [userProfile.name, metadata?.author]);

  useEffect(() => {
    localStorage.setItem('chronicle_user_profile', JSON.stringify(userProfile));
  }, [userProfile]);

  useEffect(() => {
    localStorage.setItem('chronicle_manuscript_font', manuscriptFont);
    const root = window.document.documentElement;
    root.style.setProperty('--manuscript-font', manuscriptFont);
  }, [manuscriptFont]);

  useEffect(() => {
    localStorage.setItem('chronicle_autocomplete', isAutocompleteEnabled.toString());
  }, [isAutocompleteEnabled]);

  useEffect(() => {
    localStorage.setItem('chronicle_grammar_check', isGrammarCheckEnabled.toString());
  }, [isGrammarCheckEnabled]);

  useEffect(() => {
    localStorage.setItem('chronicle_autocorrect', isAutoCorrectEnabled.toString());
  }, [isAutoCorrectEnabled]);

  useEffect(() => {
    localStorage.setItem('chronicle_zen_mode', isZenModeEnabled.toString());
  }, [isZenModeEnabled]);

  useEffect(() => {
    localStorage.setItem('chronicle_first_line_indent', isFirstLineIndentEnabled.toString());
  }, [isFirstLineIndentEnabled]);

  useEffect(() => {
    localStorage.setItem('chronicle_export_settings', JSON.stringify(exportSettings));
  }, [exportSettings]);

  useEffect(() => {
    localStorage.setItem('chronicle_theme', isDarkMode ? 'dark' : 'light');
    const root = window.document.documentElement;
    if (isDarkMode) root.classList.add('dark');
    else root.classList.remove('dark');
  }, [isDarkMode]);

  // Mirror preferences to the server (debounced) whenever any of them change.
  // localStorage alone is per-browser and evictable, which read as "settings
  // reset after the update"; /api/settings is the durable copy, hydrated back
  // into localStorage in main.tsx before render. Declared AFTER all the
  // individual persist effects above so the snapshot reads fresh values.
  useEffect(() => {
    scheduleSettingsPush();
  }, [
    isDarkMode,
    isAutocompleteEnabled,
    isAutoCorrectEnabled,
    isGrammarCheckEnabled,
    isZenModeEnabled,
    isFirstLineIndentEnabled,
    touchControlsMode,
    manuscriptFont,
    exportSettings,
    userProfile,
  ]);

  const handleCreateNew = async () => {
    const id = Math.random().toString(36).substr(2, 9);
    const newManuscript: Manuscript = {
      metadata: {
        id,
        title: '',
        author: userProfile.name || 'Uncredited Author',
        lastModified: Date.now(),
      },
      chapters: [
        {
          id: '1',
          title: 'Chapter 1',
          content: '',
          lastModified: Date.now(),
        }
      ]
    };

    try {
      await manuscriptService.create(newManuscript);
      openManuscript(id);
    } catch (error) {
      console.error(error);
      alert('Failed to create manuscript');
    }
  };

  const handleImportManuscript = async (manuscript: Manuscript) => {
    // Fill in author from profile if not set
    if (!manuscript.metadata.author) {
      manuscript.metadata.author = userProfile.name || 'Uncredited Author';
    }

    // Persist only — the import dialog owns the success/failure UI and lets
    // the user choose whether to open the manuscript, so no navigation here.
    // Errors propagate to the dialog's failure view.
    await manuscriptService.create(manuscript);
  };

  const handleUpdateChapterContent = useCallback((title: string, content: string) => {
    // Store exactly what the writer typed — including an empty title. The
    // placeholder ("Untitled Chapter", "Untitled Manuscript", "Anonymous") is a
    // DISPLAY concern and every render/export site already falls back on its
    // own (Sidebar, LibraryView, epub/docx export). Coercing here instead fed
    // the placeholder back into the editor via the title/content sync effects,
    // so clearing a title re-typed "Untitled Chapter" into the field and the
    // writer could never empty it. Keep the interactive value raw.
    if (currentChapterId === 'title-page') {
      setMetadata(prev => prev ? ({
        ...prev,
        title,
        author: content,
        lastModified: Date.now()
      }) : null);
      return;
    }
    setChapters(prev => prev.map(c =>
      c.id === currentChapterId
        ? { ...c, content, title, lastModified: Date.now() }
        : c
    ));
    setMetadata(prev => prev ? ({ ...prev, lastModified: Date.now() }) : null);
  }, [currentChapterId]);

  const handleAddChapter = useCallback(() => {
    const newChapter: Chapter = {
      id: Math.random().toString(36).substr(2, 9),
      // Empty, not the literal "Untitled Chapter": the title field then shows
      // its placeholder and the sidebar renders the faded fallback, so the
      // writer types straight into an empty field instead of first deleting a
      // placeholder that was masquerading as real text.
      title: '',
      content: '<p>Start writing...</p>',
      lastModified: Date.now(),
    };
    setChapters(prev => [...prev, newChapter]);
    setCurrentChapterId(newChapter.id);
  }, []);

  const handleSelectChapter = useCallback((id: string) => {
    setCurrentChapterId(id);
    setIsSidebarOpen(false);
  }, []);

  const handleDuplicateChapter = useCallback((id: string) => {
    const chapterToDuplicate = chapters.find(c => c.id === id);
    if (!chapterToDuplicate) return;

    const newChapter: Chapter = {
      ...chapterToDuplicate,
      id: Math.random().toString(36).substr(2, 9),
      title: `${chapterToDuplicate.title} (Copy)`,
      lastModified: Date.now(),
    };

    setChapters(prev => {
      const index = prev.findIndex(c => c.id === id);
      const nextChapters = [...prev];
      nextChapters.splice(index + 1, 0, newChapter);
      return nextChapters;
    });
    setCurrentChapterId(newChapter.id);
  }, [chapters]);

  const handleReorderChapters = useCallback((newChapters: Chapter[]) => {
    const now = Date.now();
    setChapters((previous) => {
      const oldPositions = new Map(previous.map((chapter, index) => [chapter.id, index]));
      return newChapters.map((chapter, index) =>
        oldPositions.get(chapter.id) !== index
          ? { ...chapter, lastModified: now }
          : chapter,
      );
    });
    setMetadata((prev) => prev ? { ...prev, lastModified: now } : prev);
  }, []);

  const deletingChaptersRef = useRef(new Set<string>());
  const handleDeleteChapter = useCallback(async (id: string) => {
    const session = openSessionRef.current;
    const chapter = chapters.find((candidate) => candidate.id === id);
    if (!session || !chapter || chapters.length <= 1 || deletingChaptersRef.current.has(id)) return;

    deletingChaptersRef.current.add(id);
    try {
      // An older in-flight whole-manuscript PUT still contains this chapter.
      // Finish it before DELETE so it cannot recreate the row afterward.
      const flushed = await autosave.flush();
      if (!flushed) {
        alert('Chronicle could not save the manuscript, so the chapter was not deleted. Retry after the save issue is resolved.');
        return;
      }
      if (openSessionRef.current?.key !== session.key) return;
      autosave.pause();

      let retry = true;
      while (retry) {
        try {
          const deleted = await manuscriptService.deleteChapter(
            session.manuscriptId,
            id,
            chapterRevisionsRef.current.get(id) ?? chapter.revision,
          );
          if (openSessionRef.current?.key !== session.key) return;
          chapterRevisionsRef.current.delete(id);
          if (deleted.manuscriptRevision !== undefined) {
            metadataRevisionRef.current = deleted.manuscriptRevision;
          }
          setChapters((prev) => {
            const filtered = prev.filter((candidate) => candidate.id !== id);
            if (currentChapterId === id && filtered.length > 0) setCurrentChapterId(filtered[0].id);
            return filtered;
          });
          setMetadata((prev) => prev ? {
            ...prev,
            lastModified: Date.now(),
            revision: deleted.manuscriptRevision ?? prev.revision,
          } : prev);
          return;
        } catch (error) {
          const conflict = error instanceof ManuscriptServiceError && error.status === 409;
          if (conflict) {
            alert('This chapter changed on another device and was not deleted. Return to the library and reopen the manuscript before retrying.');
            return;
          }
          retry = window.confirm(
            `${error instanceof Error ? error.message : 'Failed to delete chapter'}. Retry the deletion?`,
          );
        }
      }
    } finally {
      autosave.resume();
      deletingChaptersRef.current.delete(id);
    }
  }, [autosave.flush, autosave.pause, autosave.resume, chapters, currentChapterId]);

  const handleDeleteManuscript = useCallback(async () => {
    if (!currentManuscriptId) return;
    const manuscriptId = currentManuscriptId;
    let deleted = false;
    autosave.pause();
    try {
      // A PUT completing after DELETE would resurrect the manuscript. Drain
      // it first, then suspend the local queue before issuing the tombstone.
      // Keep the editor mounted until DELETE succeeds so a failed request can
      // resume the exact in-memory author state rather than re-fetching it.
      await autosave.discard();
      const baseRevision = metadataRevisionRef.current ?? metadata?.revision;
      await manuscriptService.delete(manuscriptId, baseRevision);
      deleted = true;
      clearManuscriptDraft(manuscriptId);
      clearManuscriptConflictDraft(manuscriptId);
      closeManuscriptImmediately();
    } catch (error) {
      console.error(error);
      alert(`${error instanceof Error ? error.message : 'Failed to delete manuscript'}. The manuscript remains open and your local edits were not discarded.`);
    } finally {
      if (!deleted) autosave.resume();
    }
  }, [autosave.discard, autosave.pause, autosave.resume, closeManuscriptImmediately, currentManuscriptId, metadata?.revision]);

  if (currentManuscriptId && (!metadata || hydratedSessionKey !== openSession?.key)) {
    return <RouteFallback isDarkMode={isDarkMode} label="Opening manuscript…" />;
  }

  if (!currentManuscriptId || !metadata) {
    // Global Settings is a full page, not an overlay: when it's open it takes
    // over the library view entirely (closing returns to the library).
    if (isGlobalSettingsOpen) {
      return (
        <Suspense fallback={<RouteFallback isDarkMode={isDarkMode} label="Opening settings…" />}>
          <GlobalSettings
            onClose={() => setIsGlobalSettingsOpen(false)}
            isDarkMode={isDarkMode}
            onToggleTheme={() => setIsDarkMode(!isDarkMode)}
            userProfile={userProfile}
            onUpdateUserProfile={(newUserProfile) => setUserProfile(prev => ({ ...prev, ...newUserProfile }))}
            exportSettings={exportSettings}
            onUpdateExportSettings={setExportSettings}
          />
        </Suspense>
      );
    }
    return (
      <>
        <LibraryView
          onSelectManuscript={(id) => openManuscript(id)}
          onCreateNew={handleCreateNew}
          onImportManuscript={handleImportManuscript}
          // The Proofreader plugin contributes its own library-card action, so
          // core withdraws its built-in one rather than showing two icons that
          // open different proofreaders.
          onProofreadManuscript={proofreadActive ? (id) => {
            openManuscript(id, true);
          } : undefined}
          onOpenSettings={() => setIsGlobalSettingsOpen(true)}
          isDarkMode={isDarkMode}
          refreshSignal={remoteRevision}
        />
      </>
    );
  }

  // Proofread mode replaces the whole editor shell: the guided revision pass
  // owns its own editor instance per chapter. Edits flow into `chapters`
  // state, so the existing debounced autosave persists them like any edit.
  if (isProofreadMode) {
    return (
      <Suspense fallback={<RouteFallback isDarkMode={isDarkMode} label="Opening proofreader…" />}>
        <ProofreadView
          metadata={metadata}
          chapters={chapters}
          isDarkMode={isDarkMode}
          onUpdateChapter={(chapterId, content) => {
            setChapters(prev => prev.map(c => c.id === chapterId ? { ...c, content, lastModified: Date.now() } : c));
          }}
          onExit={() => { void leaveManuscript(); }}
        />
        <SaveStatus
          status={autosave.status}
          error={autosave.error}
          onRetry={autosave.retry}
          canReloadServerVersion={autosave.canReloadServerVersion}
          hasConflictRecovery={autosave.hasConflictRecovery}
          onReloadServerVersion={reloadServerVersion}
          onRestoreConflictDraft={restoreConflictDraft}
          onDiscardConflictDraft={discardConflictDraft}
        />
      </Suspense>
    );
  }

  const isTitlePage = currentChapterId === 'title-page';
  const currentChapter = isTitlePage
    ? { id: 'title-page', title: metadata.title, content: metadata.author, lastModified: metadata.lastModified }
    : (chapters.find(c => c.id === currentChapterId) || chapters[0]);

  return (
    <Suspense fallback={<RouteFallback isDarkMode={isDarkMode} label="Loading editor…" />}>
      <div className={cn(
        "relative flex w-full min-h-screen-dvh selection:bg-black/10 dark:selection:bg-white/10",
        isDarkMode ? "bg-manuscript-dark" : "bg-manuscript-light"
      )}>
        <Sidebar 
          isOpen={isSidebarOpen} 
          onToggle={() => setIsSidebarOpen(!isSidebarOpen)} 
          isDarkMode={isDarkMode}
          onToggleTheme={() => setIsDarkMode(!isDarkMode)}
          chapters={chapters}
          currentChapterId={currentChapterId}
          onSelectChapter={handleSelectChapter}
          onAddChapter={handleAddChapter}
          onDeleteChapter={handleDeleteChapter}
          onDuplicateChapter={handleDuplicateChapter}
          onReorderChapters={handleReorderChapters}
          manuscriptFont={manuscriptFont}
          onChangeFont={setManuscriptFont}
          isZenModeEnabled={isZenModeEnabled}
          onToggleZenMode={() => setIsZenModeEnabled(!isZenModeEnabled)}
          isFirstLineIndentEnabled={isFirstLineIndentEnabled}
          onToggleFirstLineIndent={() => setIsFirstLineIndentEnabled(!isFirstLineIndentEnabled)}
          touchControlsMode={touchControlsMode}
          onChangeTouchControls={setTouchControlsMode}
          metadata={metadata}
          isAutocompleteEnabled={isAutocompleteEnabled}
          onToggleAutocomplete={() => setIsAutocompleteEnabled(!isAutocompleteEnabled)}
          isGrammarCheckEnabled={isGrammarCheckEnabled}
          onToggleGrammarCheck={() => setIsGrammarCheckEnabled(!isGrammarCheckEnabled)}
          isAutoCorrectEnabled={isAutoCorrectEnabled}
          onToggleAutoCorrect={() => setIsAutoCorrectEnabled(!isAutoCorrectEnabled)}
          onUpdateMetadata={(newMetadata) => setMetadata(prev => prev ? ({ ...prev, ...newMetadata }) : null)}
          userProfile={userProfile}
          onUpdateUserProfile={(newUserProfile) => setUserProfile(prev => ({ ...prev, ...newUserProfile }))}
          onDeleteManuscript={handleDeleteManuscript}
          onReturnToLibrary={() => { void leaveManuscript(); }}
          manuscriptWordCount={totalWordCount}
          exportSettings={exportSettings}
        />
        
        <main className="flex-1">
          <m.div
            key={currentChapterId}
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            transition={{ duration: 1.0 }}
            className="relative"
          >
            <EditorView 
              isDarkMode={isDarkMode} 
              onToggleTheme={() => setIsDarkMode(!isDarkMode)} 
              manuscriptId={currentManuscriptId!}
              chapterId={currentChapter.id}
              isTitlePage={isTitlePage}
              coverArt={metadata.coverArt}
              isSidebarOpen={isSidebarOpen}
              isZenModeEnabled={isZenModeEnabled}
              isAutocompleteEnabled={isAutocompleteEnabled}
              isGrammarCheckEnabled={grammarCheckActive}
              isAutoCorrectEnabled={autoCorrectActive}
              isFirstLineIndentEnabled={isFirstLineIndentEnabled}
              isTouchUI={isTouchUI}
              manuscriptFont={manuscriptFont}
              sceneBreakStyle={metadata.sceneBreakStyle || 'classic'}
              customSceneBreakSvg={metadata.customSceneBreakSvg}
              title={currentChapter.title}
              content={currentChapter.content}
              lastModified={currentChapter.lastModified}
              onUpdate={handleUpdateChapterContent}
              onEditorReady={setActiveEditor}
            />
          </m.div>
        </main>

        {isSidebarOpen && (
          <div
            className="fixed inset-0 bg-black/10 backdrop-blur-sm z-40"
            onClick={() => setIsSidebarOpen(false)}
          />
        )}
      </div>
      <SaveStatus
        status={autosave.status}
        error={autosave.error}
        onRetry={autosave.retry}
        canReloadServerVersion={autosave.canReloadServerVersion}
        hasConflictRecovery={autosave.hasConflictRecovery}
        onReloadServerVersion={reloadServerVersion}
        onRestoreConflictDraft={restoreConflictDraft}
        onDiscardConflictDraft={discardConflictDraft}
      />
    </Suspense>
  );
}

/**
 * The plugin host wraps the entire app exactly once, ABOVE every view branch.
 * Plugins therefore load a single time and survive navigation between the
 * library, the editor, and full-page views — v1 re-mounted its provider inside
 * each branch, re-importing every plugin on every navigation.
 *
 * PluginViewHost renders a plugin's full-page `view` over the app when one is
 * routed to (the slot a migrated Proofreader would use).
 */
export default function App() {
  return (
    <LazyMotion features={loadMotionFeatures}>
      <PluginHost>
        <AppInner />
        <PluginViewHost />
      </PluginHost>
    </LazyMotion>
  );
}

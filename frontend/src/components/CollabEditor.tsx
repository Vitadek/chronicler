import React, { useEffect, useState } from 'react';
import { EditorContent, useEditor, type Editor } from '@tiptap/react';
import { HocuspocusProvider } from '@hocuspocus/provider';
import Collaboration from '@tiptap/extension-collaboration';
import * as Y from 'yjs';
import { buildCoreExtensions, EDITOR_KEYBOARD_ATTRS } from '../lib/editorExtensions';

interface CollabEditorProps {
  /** User-scoped server document name: `<encoded-user>/<manuscript>:<chapter>`. */
  docName: string;
  /** WebSocket base for the collab endpoint, e.g. wss://host/collab */
  collabUrl: string;
  /** Bearer token if the collab socket needs auth (OIDC phase). */
  token?: string;
  className?: string;
  /** Hands the live TipTap editor up to the parent — same contract as
   *  EditorView's onEditorReady — so App's activeEditor (Comments panel,
   *  plugin runtime, selection actions) targets the editor the user is
   *  actually typing in instead of a detached non-collab instance. */
  onEditorReady?: (editor: Editor | null) => void;
}

interface CollabSession {
  docName: string;
  ydoc: Y.Doc;
  provider: HocuspocusProvider;
}

/**
 * Real-time collaborative editor: one Y.Doc per document name, synced to the
 * server's Hocuspocus /collab endpoint. Content lives in the Y.Doc (not an HTML
 * prop), so two clients on the same docName edit live. Reuses the shared core
 * extensions, so it keeps the app's typography + keyboard behavior.
 *
 * A fresh chapter Y.Doc is seeded from the authoritative chapter HTML by the
 * server before live updates begin.
 *
 * The Y.Doc/provider are constructed inside an EFFECT, not render (useMemo):
 * StrictMode double-invokes useMemo factories in dev, and React may discard a
 * render's memoized values in concurrent mode — both would leak a live
 * WebSocket connection with only the committed instance ever torn down. An
 * effect guarantees exactly one live socket per committed mount.
 */
export const CollabEditor: React.FC<CollabEditorProps> = ({
  docName,
  collabUrl,
  token,
  className,
  onEditorReady,
}) => {
  const [session, setSession] = useState<CollabSession | null>(null);
  const [status, setStatus] = useState<string>('connecting');

  useEffect(() => {
    const ydoc = new Y.Doc();
    const provider = new HocuspocusProvider({
      url: collabUrl,
      name: docName,
      document: ydoc,
      token,
      onStatus: ({ status }: { status: string }) => setStatus(status),
    });
    setStatus('connecting');
    setSession({ docName, ydoc, provider });
    return () => {
      provider.destroy();
      ydoc.destroy();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [collabUrl, docName, token]);

  // Deliberately NOT wired to App's autosave: the server is already the sole
  // persister for this chapter's content via the Hocuspocus snapshot (see
  // useManuscriptAutosave's collabActiveChapterId exclusion in App.tsx). A
  // naive onUpdate bridge here would resurface as chronic 409s, because the
  // server bumps this chapter's revision on every collab snapshot and every
  // remote collaborator's keystroke would also replay as local autosave
  // traffic via the shared Y.Doc's 'update' event.
  const editor = useEditor(
    session
      ? {
          extensions: buildCoreExtensions({
            collabExtension: Collaboration.configure({ document: session.ydoc }),
          }),
          editorProps: {
            attributes: {
              class: 'novel-editor-content focus:outline-none min-h-[500px]',
              ...EDITOR_KEYBOARD_ATTRS,
            },
          },
        }
      : { extensions: [], editable: false },
    [session],
  );

  useEffect(() => {
    onEditorReady?.(session ? editor : null);
    return () => onEditorReady?.(null);
  }, [session, editor, onEditorReady]);

  return (
    <div className={className}>
      <div className="text-[10px] uppercase tracking-[0.2em] font-bold opacity-30 mb-3">
        Live · {status}
      </div>
      {session ? (
        <EditorContent editor={editor} className="w-full" />
      ) : (
        <div className="min-h-[500px] opacity-40">Connecting collaborative editor…</div>
      )}
    </div>
  );
};

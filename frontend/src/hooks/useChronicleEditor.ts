import { useMemo, useRef } from 'react';
import { useEditor } from '@tiptap/react';
import Focus from '@tiptap/extension-focus';
import BubbleMenuExtension from '@tiptap/extension-bubble-menu';
import { CommandLine } from '../lib/CommandLine';
import { buildCoreExtensions, EDITOR_KEYBOARD_ATTRS } from '../lib/editorExtensions';
import type { AnyExtension, Editor } from '@tiptap/core';

export interface UseChronicleEditorProps {
  content?: string;
  /** Receives the live editor rather than serialized HTML. Callers that mirror
   * content into React can serialize on their own debounce boundary, keeping
   * whole-document getHTML() work out of the typing transaction. */
  onUpdate?: (editor: Editor) => void;
  placeholder?: string;
  className?: string;
  commandLineOptions?: any;
  /**
   * TipTap extensions contributed by enabled plugins (the `editorExtensions`
   * slot). Passed in by the caller rather than read from context here, so this
   * hook stays usable outside the plugin host (e.g. the mobile editor bundle).
   * This is the seam editor checkers and other extensions move through.
   */
  pluginExtensions?: AnyExtension[];
  /**
   * Constrain the editor to a single, indestructible H1 (the chapter/manuscript
   * title). See buildCoreExtensions' CoreExtensionOptions.singleLineHeading.
   */
  singleLineHeading?: boolean;
}

export function useChronicleEditor({ 
  content = '', 
  onUpdate, 
  placeholder = 'Once upon a time...',
  className = 'novel-editor-content focus:outline-none min-h-[500px]',
  commandLineOptions,
  pluginExtensions,
  singleLineHeading = false,
}: UseChronicleEditorProps) {
  // Core prose + marks come from the shared module so the mobile editor bundle
  // stays in sync (smart quotes, no-stray-space, marks). The web-only
  // interactive layer (focus dimming, the #! command portal, and selection
  // bubble) is layered on top here.
  const extensions = useMemo(() => [
    ...buildCoreExtensions({ placeholder, singleLineHeading }),
    Focus.configure({
      className: 'has-focus',
      mode: 'all',
    }),
    CommandLine.configure({
      suggestion: {
        char: '#!',
        allowSpaces: true,
        ...commandLineOptions,
      },
    }),
    BubbleMenuExtension.configure({
      shouldShow: ({ state, from, to }) => {
        return from !== to && state.doc.textBetween(from, to).trim().length > 0;
      },
    }),
    // Plugin-contributed extensions last, so a plugin can layer on top of the
    // core ones. TipTap can't hot-swap extensions, so the editor is re-created
    // when this set changes (enabling/disabling a plugin) — see the dep below.
    ...(pluginExtensions ?? []),
    // eslint-disable-next-line react-hooks/exhaustive-deps
  ], [placeholder, pluginExtensions, singleLineHeading]);

  // useEditor() has no deps array, so @tiptap/react re-runs its options-compare
  // effect after every render. A fresh `editorProps` object literal and the
  // `content` prop (which App echoes back on every keystroke) both fail the
  // `!==` compare every time, triggering a redundant editor.setOptions() ->
  // view.setProps + view.updateState pass. Memoizing editorProps and freezing
  // the creation-time content stops that: setOptions never re-applies content
  // after creation anyway (chapter switches go through EditorView's own sync
  // effect, and ProofreadView remounts per chapter via key={chapter.id}).
  const editorProps = useMemo(() => ({
    attributes: {
      class: className,
      // Shared keyboard-control attributes (see lib/editorExtensions.ts):
      // make Chronicler's Typography the single source of smart punctuation
      // and stop the OS keyboard from injecting a stray space after quotes.
      ...EDITOR_KEYBOARD_ATTRS,
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }), [className]);
  const initialContent = useRef(content).current;

  const editor = useEditor({
    extensions,
    content: initialContent,
    onUpdate: ({ editor }) => {
      onUpdate?.(editor);
    },
    editorProps,
  });

  return editor;
}

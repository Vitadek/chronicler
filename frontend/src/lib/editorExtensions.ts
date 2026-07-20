import StarterKit from '@tiptap/starter-kit';
import Document from '@tiptap/extension-document';
import { Extension } from '@tiptap/core';
import Placeholder from '@tiptap/extension-placeholder';
import CharacterCount from '@tiptap/extension-character-count';
import Typography from '@tiptap/extension-typography';
import { AutoCorrect } from './AutoCorrect';
import { Epigraph } from './Epigraph';
import { CommentMark } from './Comment';
import { AudioMark } from './Audio';
import type { AnyExtension } from '@tiptap/core';
import type { Doc as YDoc } from 'yjs';

/**
 * Keyboard-control attributes applied to the editor's contenteditable.
 *
 * Chronicle's Typography extension is the SINGLE source of smart punctuation
 * (curly quotes, em dashes, ellipses). Letting the OS keyboard also
 * autocorrect/auto-punctuate fights those input rules and drops a stray space
 * after quotes on mobile. We turn the keyboard's active text rewriting off,
 * keep spellcheck on (passive red squiggles, never mutates text), and keep
 * sentence auto-capitalization (only changes case).
 *
 * Shared by the web editor (src/hooks/useChronicleEditor.ts) and the mobile
 * slim editor bundle so both surfaces behave identically.
 */
export const EDITOR_KEYBOARD_ATTRS = {
  autocapitalize: 'sentences',
  autocorrect: 'off',
  autocomplete: 'off',
  spellcheck: 'true',
} as const;

export interface CoreExtensionOptions {
  placeholder?: string;
  /**
   * Kept for callers that only have the raw Y.Doc (reserved for a future mobile
   * bridge provider) — when set, StarterKit's undo/redo is disabled the same as
   * `collabExtension`, since either signals collaboration is active. Does NOT by
   * itself add Yjs bindings; pass `collabExtension` for that.
   */
  collabDocument?: YDoc;
  /**
   * A pre-built `@tiptap/extension-collaboration` instance bound to the shared
   * Y.Doc. `@tiptap/extension-collaboration` (and the yjs/lib0 stack it pulls
   * in, ~103K min) is NOT imported here — only the lazy CollabEditor chunk
   * imports it and passes the built extension in, so solo-writing sessions and
   * the eager entry chunk never pay the collaboration bundle cost. StarterKit's
   * undo/redo is disabled whenever this (or `collabDocument`) is set, since
   * Collaboration brings its own Yjs-aware history and the two must not both be
   * active.
   */
  collabExtension?: AnyExtension;
  /**
   * Constrain the document to exactly one, indestructible H1 — the chapter /
   * manuscript title field. Swaps StarterKit's `block+` Document for one whose
   * content is `heading` (not `heading+`), pins Heading to level 1, and blocks
   * Enter so the title can never split into extra blocks or lose its H1 type.
   *
   * Without this, Ctrl+A + Delete removes the heading node and ProseMirror
   * backfills a paragraph, silently demoting the title to plain text. Body
   * editors leave this off and keep the full multi-block prose schema.
   */
  singleLineHeading?: boolean;
}

/**
 * The prose core shared by every Chronicle editor surface: StarterKit, smart
 * typography, underline, placeholder, word counting, and the inline marks
 * (epigraph / comment / audio).
 *
 * The web-only interactive layer — Focus dimming, the Autocomplete ghost-text,
 * the `#!` CommandLine portal, and the selection BubbleMenu — is added on top
 * in useChronicleEditor. The mobile bundle leaves those out and drives the
 * equivalent affordances from native Flutter UI over the JS bridge.
 */
export function buildCoreExtensions(
  {
    placeholder = 'Once upon a time...',
    collabDocument,
    collabExtension,
    singleLineHeading = false,
  }: CoreExtensionOptions = {},
): AnyExtension[] {
  const extensions: AnyExtension[] = [
    // NOTE: StarterKit already registers Underline. Importing it again here
    // produced a "Duplicate extension names found: ['underline']" warning and
    // registered the mark twice (caught by scripts/schemaRoundTrip.test.ts).
    StarterKit.configure({
      // Title editor: disable StarterKit's block+ Document (replaced below with a
      // single-heading one) and pin Heading to level 1. Body keeps levels 1–3.
      ...(singleLineHeading
        ? { document: false, heading: { levels: [1] } }
        : { heading: { levels: [1, 2, 3] } }),
      // Collaboration owns undo/redo via the Yjs undo manager; the two must
      // not both be active or history desyncs from the shared doc.
      ...(collabDocument || collabExtension ? { undoRedo: false } : {}),
    }),
    Placeholder.configure({ placeholder }),
    CharacterCount,
    Typography,
    // Deterministic autocorrect + sentence-start capitalization. Sits right after
    // Typography so both share the single-Backspace-undo input-rule behavior.
    AutoCorrect,
    Epigraph,
    CommentMark,
    AudioMark,
  ];
  if (singleLineHeading) {
    // A Document whose content is exactly `heading` (one node, no siblings): the
    // heading can't be deleted (delete-all just empties its text) and Enter's
    // splitBlock becomes schema-illegal — a silent no-op. Registered first, and
    // paired with a keymap that swallows the newline shortcuts outright.
    extensions.unshift(
      Document.extend({ content: 'heading' }),
      Extension.create({
        name: 'titleSingleLine',
        addKeyboardShortcuts() {
          const swallow = () => true;
          return { Enter: swallow, 'Mod-Enter': swallow, 'Shift-Enter': swallow };
        },
      }),
    );
  }
  if (collabExtension) {
    extensions.push(collabExtension);
  }
  return extensions;
}

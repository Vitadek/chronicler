import { Mark, mergeAttributes } from '@tiptap/core';
import { Plugin, PluginKey } from '@tiptap/pm/state';
import type { EditorState } from '@tiptap/pm/state';
import { Decoration, DecorationSet } from '@tiptap/pm/view';

/**
 * Inline audio attachment.
 *
 * Legacy document-schema support for manuscripts containing audio marks.
 * Marks a span of text and stashes a session-scoped blob URL handle
 * so a floating play button can be rendered next to the marked text.
 *
 * Storage strategy:
 *   - The attribute value is an *opaque token* — we don't serialize the
 *     blob URL into the document HTML, because (a) blob URLs are per-tab
 *     and would dead-link, (b) we don't want audio surviving into a
 *     manuscript export.
 *   - The actual blob URL lives in a module-level Map keyed by the token.
 *     When a caller provides audio, it registers the URL; when the play
 *     widget is clicked, we look it up.
 *
 * Audio is session-scoped: it doesn't survive a reload. That's fine and
 * deliberate; old manuscripts retain their mark attributes without loss.
 */

const audioStore = new Map<string, string>(); // token -> blob URL

// The currently-playing/paused HTMLAudioElement per token. Kept module-level
// (rather than stashed on the widget DOM node) so playback survives widget
// redraws — e.g. retyping while narration is playing no longer orphans the
// element and spawns a second overlapping Audio on the next click.
const audioPlaybackStore = new Map<string, HTMLAudioElement>();

export function registerAudioToken(token: string, blobUrl: string): void {
  // Revoke any prior URL associated with this token so blob memory frees.
  const old = audioStore.get(token);
  if (old && old !== blobUrl) URL.revokeObjectURL(old);
  audioStore.set(token, blobUrl);
}

export function getAudioForToken(token: string): string | undefined {
  return audioStore.get(token);
}

export function clearAudioStore(): void {
  for (const url of audioStore.values()) URL.revokeObjectURL(url);
  audioStore.clear();
  for (const audio of audioPlaybackStore.values()) {
    audio.pause();
    audio.src = '';
  }
  audioPlaybackStore.clear();
}

export function newAudioToken(): string {
  return 'audio_' + Date.now().toString(36) + '_' + Math.random().toString(36).slice(2, 9);
}

const audioDecorationsKey = new PluginKey<DecorationSet>('audioDecorations');

// Lazily builds the widget DOM only when prosemirror-view actually needs a
// new node for this token — with a stable `key` below, unchanged audio
// markers reuse their existing widget across transactions instead of being
// torn down and recreated every keystroke.
function buildWidget(token: string) {
  return () => {
    const widget = document.createElement('button');
    widget.type = 'button';
    widget.className = 'audio-icon-widget';
    widget.setAttribute('data-audio-token', token);
    widget.setAttribute('title', 'Play attached audio');
    widget.innerHTML = `
      <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="opacity: 0.4; margin-left: 12px; display: inline-block; cursor: pointer; pointer-events: auto;">
        <polygon points="5 3 19 12 5 21 5 3"></polygon>
      </svg>
    `;

    // Reflect any already-in-progress playback for this token — the widget
    // may be a fresh DOM node (e.g. after a doc change moved the block) even
    // though the underlying Audio element has been playing the whole time.
    const existing = audioPlaybackStore.get(token);
    if (existing && !existing.paused) widget.classList.add('playing');

    // Inline click handler: lookup, lazily create an Audio element,
    // toggle play/pause. We don't reuse a global audio so multiple
    // markers can be played in sequence without sharing state.
    widget.addEventListener('click', (e) => {
      e.preventDefault();
      e.stopPropagation();
      const url = audioStore.get(token);
      if (!url) {
        console.warn('No audio for token', token);
        return;
      }
      let audio = audioPlaybackStore.get(token);
      if (!audio) {
        audio = new Audio(url);
        audioPlaybackStore.set(token, audio);
        audio.addEventListener('ended', () => {
          // The widget currently in the DOM for this token may not be the
          // same node that started playback if the doc changed meanwhile.
          document
            .querySelectorAll(`[data-audio-token="${token}"]`)
            .forEach(el => el.classList.remove('playing'));
        });
      }
      if (audio.paused) {
        audio.play().catch(err => console.warn('audio play failed', err));
        widget.classList.add('playing');
      } else {
        audio.pause();
        widget.classList.remove('playing');
      }
    });

    return widget;
  };
}

function computeAudioDecorations(state: EditorState): DecorationSet {
  const { doc } = state;
  const decorations: Decoration[] = [];

  doc.descendants((node, pos) => {
    if (!node.isBlock) return;
    let hasAudio = false;
    let token = '';
    node.descendants(child => {
      const m = child.marks.find(mk => mk.type.name === 'audio');
      if (m) {
        hasAudio = true;
        token = m.attrs.token;
        return false;
      }
    });
    if (!hasAudio) return;

    decorations.push(
      Decoration.widget(pos + node.nodeSize - 1, buildWidget(token), {
        side: 1,
        key: `audio:${token}`,
      }),
    );
  });

  return DecorationSet.create(doc, decorations);
}

export const AudioMark = Mark.create({
  name: 'audio',
  inclusive: false,

  addOptions() {
    return {
      HTMLAttributes: {
        class: 'manuscript-audio-marker',
      },
    };
  },

  addAttributes() {
    return {
      token: {
        default: null,
        parseHTML: element => element.getAttribute('data-audio-token'),
        renderHTML: attributes => {
          if (!attributes.token) return {};
          return { 'data-audio-token': attributes.token };
        },
      },
    };
  },

  parseHTML() {
    return [
      { tag: 'span[data-audio-token]' },
    ];
  },

  renderHTML({ HTMLAttributes }) {
    return ['span', mergeAttributes(this.options.HTMLAttributes, HTMLAttributes), 0];
  },

  addProseMirrorPlugins() {
    return [
      new Plugin<DecorationSet>({
        key: audioDecorationsKey,
        state: {
          init: (_, state) => computeAudioDecorations(state),
          apply(tr, old, _oldState, newState) {
            if (!tr.docChanged) return old.map(tr.mapping, tr.doc);
            return computeAudioDecorations(newState);
          },
        },
        props: {
          decorations(state) {
            return audioDecorationsKey.getState(state);
          },
        },
      }),
    ];
  },
});

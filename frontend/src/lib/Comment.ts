import { Mark, mergeAttributes } from '@tiptap/core';
import { Plugin, PluginKey } from '@tiptap/pm/state';
import type { EditorState } from '@tiptap/pm/state';
import { Decoration, DecorationSet } from '@tiptap/pm/view';
import type { EditorView } from '@tiptap/pm/view';
import tippy from 'tippy.js';
import 'tippy.js/dist/tippy.css';
import 'tippy.js/animations/shift-away.css';

interface CommentRange {
  text: string;
  from: number;
  to: number;
}

const commentDecorationsKey = new PluginKey<DecorationSet>('commentDecorations');

// Lazily builds the widget DOM (including the tippy instance) only when
// prosemirror-view actually needs a new node for this range — with a stable
// `key` below, unchanged ranges reuse their existing widget across
// transactions instead of tearing down and reconstructing it every keystroke.
function buildWidget(range: CommentRange) {
  return (view: EditorView) => {
    const widget = document.createElement('span');
    widget.className = 'comment-icon-widget';
    widget.style.cursor = 'pointer';

    // Helper to trigger the edit UI
    const triggerEdit = (e: Event) => {
      e.preventDefault();
      e.stopPropagation();
      const event = new CustomEvent('edit-comment', {
        detail: {
          from: range.from,
          to: range.to,
          comment: range.text,
          text: view.state.doc.textBetween(range.from, range.to),
        },
      });
      window.dispatchEvent(event);
    };

    // Double click for the full popup
    widget.addEventListener('dblclick', triggerEdit);

    // Single click/tap tooltip to "see what the comment said"
    tippy(widget, {
      content: range.text || '(empty comment)',
      placement: 'top',
      animation: 'shift-away',
      theme: 'manuscript',
      maxWidth: 300,
    });

    widget.innerHTML = `
      <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="opacity: 0.3; margin-left: 4px; margin-right: 4px; display: inline-block; pointer-events: none;">
        <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"></path>
      </svg>
    `;

    return widget;
  };
}

function computeCommentDecorations(state: EditorState): DecorationSet {
  const { doc } = state;
  const decorations: Decoration[] = [];

  doc.descendants((node, pos) => {
    if (node.isBlock) {
      const commentRanges: CommentRange[] = [];
      let currentRange: CommentRange | null = null;

      node.descendants((child, childPos) => {
        if (!child.isText) return;
        const mark = child.marks.find(m => m.type.name === 'comment');
        const commentText = mark?.attrs.comment;
        const childFrom = pos + childPos + 1;
        const childTo = childFrom + child.nodeSize;

        if (mark && commentText) {
          if (currentRange && currentRange.text === commentText && currentRange.to === childFrom) {
            currentRange.to = childTo;
          } else {
            currentRange = { text: commentText, from: childFrom, to: childTo };
            commentRanges.push(currentRange);
          }
        } else {
          currentRange = null;
        }
      });

      commentRanges.forEach(range => {
        decorations.push(
          Decoration.widget(range.to, buildWidget(range), {
            side: 1,
            key: `comment:${range.from}:${range.text}`,
            destroy: (node) => (node as { _tippy?: { destroy: () => void } })._tippy?.destroy(),
          }),
        );
      });
    }
  });

  return DecorationSet.create(doc, decorations);
}

export const CommentMark = Mark.create({
  name: 'comment',
  inclusive: false,

  addOptions() {
    return {
      HTMLAttributes: {
        class: 'manuscript-comment-marker',
      },
    };
  },

  addAttributes() {
    return {
      comment: {
        default: null,
        parseHTML: element => element.getAttribute('data-comment'),
        renderHTML: attributes => {
          return {
            'data-comment': attributes.comment,
          };
        },
      },
    };
  },

  parseHTML() {
    return [
      {
        tag: 'span[data-comment]',
      },
    ];
  },

  renderHTML({ HTMLAttributes }) {
    return ['span', mergeAttributes(this.options.HTMLAttributes, HTMLAttributes), 0];
  },

  addProseMirrorPlugins() {
    return [
      new Plugin<DecorationSet>({
        key: commentDecorationsKey,
        state: {
          init: (_, state) => computeCommentDecorations(state),
          apply(tr, old, _oldState, newState) {
            if (!tr.docChanged) return old.map(tr.mapping, tr.doc);
            return computeCommentDecorations(newState);
          },
        },
        props: {
          decorations(state) {
            return commentDecorationsKey.getState(state);
          },
        },
      }),
    ];
  },
});

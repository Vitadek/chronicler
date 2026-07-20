import React, { useEffect, useState } from 'react';
import type { Editor } from '@tiptap/react';
import { BubbleMenu } from '@tiptap/react/menus';
import type { EditorState } from '@tiptap/pm/state';
import { Bold, Italic, Underline } from 'lucide-react';
import { cn } from '../lib/utils';

const shouldShow = ({ state, from, to }: { state: EditorState; from: number; to: number }) =>
  from !== to && state.doc.textBetween(from, to).trim().length > 0;

interface FormattingToolbarProps {
  editor: Editor | null;
  isDarkMode: boolean;
  pluginKey: string;
  isTouchUI?: boolean;
}

const Controls = ({ editor, isDarkMode, large = false }: {
  editor: Editor;
  isDarkMode: boolean;
  large?: boolean;
}) => {
  const actions = [
    { mark: 'bold', title: 'Bold', icon: Bold, run: () => editor.chain().focus().toggleBold().run() },
    { mark: 'italic', title: 'Italic', icon: Italic, run: () => editor.chain().focus().toggleItalic().run() },
    { mark: 'underline', title: 'Underline', icon: Underline, run: () => editor.chain().focus().toggleUnderline().run() },
  ];
  return (
    <div className="flex items-center">
      {actions.map(({ mark, title, icon: Icon, run }) => (
        <button
          key={mark}
          type="button"
          onClick={run}
          className={cn(
            large ? 'p-3' : 'p-2',
            'rounded-full transition-colors',
            editor.isActive(mark)
              ? isDarkMode ? 'bg-white/10 text-white' : 'bg-black/5 text-black'
              : 'opacity-60 hover:opacity-100 hover:bg-black/5 dark:hover:bg-white/5',
          )}
          title={title}
          aria-label={title}
        >
          <Icon className={large ? 'w-5 h-5' : 'w-3.5 h-3.5'} />
        </button>
      ))}
    </div>
  );
};

/** Selection controls retained in core after lexical and generative actions moved to plugins. */
export const FormattingToolbar: React.FC<FormattingToolbarProps> = ({
  editor,
  isDarkMode,
  pluginKey,
  isTouchUI = false,
}) => {
  const [hasSelection, setHasSelection] = useState(false);

  useEffect(() => {
    if (!editor || !isTouchUI) return;
    const update = () => {
      const { from, to } = editor.state.selection;
      setHasSelection(from !== to && editor.state.doc.textBetween(from, to, ' ').trim().length > 0 && editor.isFocused);
    };
    update();
    editor.on('selectionUpdate', update);
    editor.on('transaction', update);
    editor.on('focus', update);
    editor.on('blur', update);
    return () => {
      editor.off('selectionUpdate', update);
      editor.off('transaction', update);
      editor.off('focus', update);
      editor.off('blur', update);
    };
  }, [editor, isTouchUI]);

  if (!editor) return null;
  const shell = cn(
    'flex items-center p-1 rounded-full border shadow-2xl backdrop-blur-md',
    isDarkMode ? 'bg-[#232220]/95 border-white/10 text-[#F1EDE4]' : 'bg-white/95 border-black/10 text-[#1A1A1A]',
  );

  if (!isTouchUI) {
    return (
      <BubbleMenu editor={editor} pluginKey={pluginKey} shouldShow={shouldShow}>
        <div className={shell} data-testid="formatting-selection-toolbar">
          <Controls editor={editor} isDarkMode={isDarkMode} />
        </div>
      </BubbleMenu>
    );
  }

  return hasSelection ? (
    <div
      className="fixed bottom-0 inset-x-0 z-[80] flex justify-center px-3 pb-3 safe-pad-bottom pointer-events-none"
      onPointerDown={(event) => event.preventDefault()}
      data-testid="formatting-selection-toolbar"
    >
      <div className={cn(shell, 'pointer-events-auto rounded-2xl px-2 py-1.5')}>
        <Controls editor={editor} isDarkMode={isDarkMode} large />
      </div>
    </div>
  ) : null;
};

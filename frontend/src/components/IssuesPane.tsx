import React, { useEffect, useMemo, useState } from 'react';
import { Clock, SpellCheck, CheckCircle2, Sparkles } from 'lucide-react';
import { cn } from '../lib/utils';
import type { TenseShiftHit } from '../lib/TenseShift';
import type { GrammarMark } from '../lib/Grammar';
import { locateQuote } from '../lib/proseMirrorText';
import { aiGrammarPass } from '../services/grammarAiService';

interface IssuesPaneProps {
  isDarkMode: boolean;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  editor: any | null;
  tenseHits: TenseShiftHit[];
  grammarMarks: GrammarMark[];
  tenseEnabled: boolean;
  grammarEnabled: boolean;
  /** Current chapter id, if the caller has one — used to reset pagination
      when the writer switches chapters so "Show more" doesn't carry over. */
  chapterId?: string;
}

interface AiRow {
  from: number | null;
  to: number | null;
  message: string;
  suggestion?: string;
  text: string;
}

interface Row {
  key: string;
  from: number | null;
  to: number | null;
  source: 'tense' | 'grammar' | 'ai';
  kind: string;
  label: string;
  text: string;
}

/**
 * Navigable list of all findings: live tense-shift + grammar squiggles plus the
 * on-demand AI pass. Clicking a row that has a position selects it and scrolls
 * it into view. The "AI pass" button runs the structural check the rule engines
 * can't (fragments / missing verbs) and paints purple squiggles for what it
 * locates.
 */
export const IssuesPane: React.FC<IssuesPaneProps> = ({
  isDarkMode,
  editor,
  tenseHits,
  grammarMarks,
  tenseEnabled,
  grammarEnabled,
  chapterId,
}) => {
  const [aiRows, setAiRows] = useState<AiRow[]>([]);
  const [aiLoading, setAiLoading] = useState(false);
  const [aiError, setAiError] = useState<string | null>(null);
  // How many rows to render. A long chapter with grammar + tense checking
  // active can produce hundreds of marks; rendering them all as buttons janks
  // the sidebar, so cap it and reveal more on demand.
  const LIST_PAGE = 50;
  const [visible, setVisible] = useState(LIST_PAGE);

  useEffect(() => {
    setVisible(LIST_PAGE);
  }, [chapterId]);

  const jump = (from: number | null, to: number | null) => {
    if (from == null || to == null || !editor || editor.isDestroyed) return;
    editor.chain().focus().setTextSelection({ from, to }).scrollIntoView().run();
  };

  const runAiPass = async () => {
    if (!editor || editor.isDestroyed || aiLoading) return;
    setAiLoading(true);
    setAiError(null);
    try {
      const issues = await aiGrammarPass(editor.getText());
      const rows: AiRow[] = issues.map((it) => {
        const pos = locateQuote(editor, it.quote);
        return {
          from: pos?.from ?? null,
          to: pos?.to ?? null,
          message: it.suggestion ? `${it.message} → ${it.suggestion}` : it.message,
          suggestion: it.suggestion,
          text: it.quote,
        };
      });
      editor
        .chain()
        .setAiMarks(
          rows
            .filter((r) => r.from != null && r.to != null)
            .map((r) => ({ from: r.from as number, to: r.to as number, message: r.message })),
        )
        .run();
      setAiRows(rows);
    } catch (e) {
      setAiError(e instanceof Error ? e.message : 'AI pass failed');
    } finally {
      setAiLoading(false);
    }
  };

  const clearAi = () => {
    if (editor && !editor.isDestroyed) editor.chain().clearAiMarks().run();
    setAiRows([]);
    setAiError(null);
  };

  const rows: Row[] = useMemo(() => [
    ...tenseHits.map((h): Row => {
      const label = `Tense drift — ${h.tense} in a ${h.expected}-tense paragraph`;
      return {
        key: `tense-${h.from}-${h.to}-${label}`,
        from: h.from,
        to: h.to,
        source: 'tense',
        kind: h.tense,
        label,
        text: h.text,
      };
    }),
    ...grammarMarks.map((m): Row => ({
      key: `grammar-${m.from}-${m.to}-${m.kind}-${m.message}`,
      from: m.from,
      to: m.to,
      source: 'grammar',
      kind: m.kind,
      label: m.message,
      text: m.text,
    })),
    ...aiRows.map((a): Row => ({
      key: `ai-${a.from}-${a.to}-${a.message}`,
      from: a.from,
      to: a.to,
      source: 'ai',
      kind: 'ai',
      label: a.message,
      text: a.text,
    })),
  ].sort((x, y) => (x.from ?? Infinity) - (y.from ?? Infinity)), [tenseHits, grammarMarks, aiRows]);

  const liveEmpty = !tenseEnabled && !grammarEnabled;

  return (
    <div className="flex flex-col flex-1 min-h-0 overflow-hidden">
      <div className="px-4 py-2 flex items-center justify-between text-[9px] uppercase tracking-[0.15em] font-bold opacity-30">
        <span>Issues</span>
        <span>{rows.length}</span>
      </div>

      {/* On-demand AI pass control */}
      <div className="px-4 pb-3 flex items-center gap-2">
        <button
          onClick={runAiPass}
          disabled={aiLoading || !editor}
          className={cn(
            'flex items-center gap-2 px-3 py-2 rounded-xl text-xs font-semibold transition-all',
            'bg-purple-600/10 text-purple-600 dark:text-purple-300 hover:bg-purple-600/20 disabled:opacity-40',
          )}
        >
          <Sparkles className={cn('w-3.5 h-3.5', aiLoading && 'animate-pulse')} />
          {aiLoading ? 'Checking…' : 'AI grammar pass'}
        </button>
        {aiRows.length > 0 && !aiLoading && (
          <button onClick={clearAi} className="text-[11px] opacity-50 hover:opacity-100">
            clear
          </button>
        )}
      </div>
      {aiError && <p className="px-4 pb-2 text-[11px] text-red-500">{aiError}</p>}

      <div className="flex-1 overflow-y-auto pr-1 space-y-1">
        {rows.length === 0 ? (
          liveEmpty && aiRows.length === 0 ? (
            <p className="px-4 py-6 text-xs opacity-40 leading-relaxed">
              Turn on <span className="font-semibold">Tense Check</span> or{' '}
              <span className="font-semibold">Grammar Check</span> in Settings for live squiggles, or
              run an <span className="font-semibold">AI grammar pass</span> above.
            </p>
          ) : (
            <div className="px-4 py-10 flex flex-col items-center gap-3 opacity-40 text-center">
              <CheckCircle2 className="w-6 h-6" />
              <p className="text-xs">No issues found.</p>
            </div>
          )
        ) : (
          <>
            {rows.slice(0, visible).map((r) => (
              <button
                key={r.key}
                onClick={() => jump(r.from, r.to)}
                disabled={r.from == null}
                className={cn(
                  'w-full text-left px-4 py-3 rounded-xl transition-all',
                  r.from != null ? 'hover:bg-black/5 dark:hover:bg-white/5' : 'cursor-default',
                )}
              >
                <div className="flex items-start gap-3">
                  {r.source === 'tense' ? (
                    <Clock className="w-3.5 h-3.5 mt-0.5 shrink-0 text-amber-500" />
                  ) : r.source === 'ai' ? (
                    <Sparkles className="w-3.5 h-3.5 mt-0.5 shrink-0 text-purple-500" />
                  ) : (
                    <SpellCheck
                      className={cn(
                        'w-3.5 h-3.5 mt-0.5 shrink-0',
                        r.kind === 'misspelling' || r.kind === 'grammar' ? 'text-red-500' : 'text-blue-500',
                      )}
                    />
                  )}
                  <div className="min-w-0">
                    <p
                      className={cn(
                        'text-[13px] leading-snug truncate font-medium',
                        isDarkMode ? 'text-white/85' : 'text-black/85',
                      )}
                    >
                      “{r.text}”
                    </p>
                    <p className="text-[11px] leading-snug opacity-50 mt-0.5">{r.label}</p>
                  </div>
                </div>
              </button>
            ))}
            {rows.length > visible && (
              <button
                onClick={() => setVisible((v) => v + LIST_PAGE)}
                className="w-full text-center px-4 py-3 rounded-xl text-[11px] opacity-50 hover:opacity-80 hover:bg-black/5 dark:hover:bg-white/5 transition-colors"
              >
                Show more ({rows.length - visible} more)
              </button>
            )}
          </>
        )}
      </div>
    </div>
  );
};

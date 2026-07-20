import React, { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react';
import { MessageSquare, Quote, Terminal } from 'lucide-react';
import { cn } from '../lib/utils';

export const CommandPortal = forwardRef((props: any, ref) => {
  const [commandText, setCommandText] = useState('');
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const timer = setTimeout(() => inputRef.current?.focus(), 10);
    return () => clearTimeout(timer);
  }, []);

  useImperativeHandle(ref, () => ({
    onKeyDown: ({ event }: { event: KeyboardEvent }) => event.key === 'Escape',
    onUpdate: () => {},
  }));

  const command = commandText.trim().replace(/^\//, '').toLowerCase();
  const isComment = command === 'comment';
  const isEpigraph = command === 'epigraph';

  return (
    <div className="z-[100] bg-[#1A1918] text-[#F1EDE4] border border-[#F1EDE4]/10 rounded-lg shadow-2xl overflow-hidden min-w-[240px] font-sans">
      <div className="flex items-center gap-2 px-3 py-2 border-b border-white/15 bg-white/[0.02]">
        <Terminal className="w-3.5 h-3.5 opacity-50" />
        <span className="text-[9px] uppercase tracking-[0.2em] font-bold opacity-30">Manuscript CLI</span>
      </div>
      <div className="p-3">
        <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-black/40 border border-white/15 mb-3">
          <span className="text-[#F1EDE4]/20 text-xs font-mono font-bold">#!</span>
          <input
            ref={inputRef}
            value={commandText}
            onChange={(event) => setCommandText(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== 'Enter' && event.key !== ' ') return;
              event.preventDefault();
              const parts = commandText.trim().split(/\s+/);
              props.command({ command: parts[0].replace(/^\//, '').toLowerCase(), args: parts.slice(1) });
            }}
            placeholder="Command..."
            className="bg-transparent border-none outline-none text-xs font-mono flex-1 text-[#F1EDE4] placeholder:opacity-10"
          />
        </div>
        <div className="space-y-1">
          <CommandRow active={isComment} icon={MessageSquare} label="/COMMENT" />
          <CommandRow active={isEpigraph} icon={Quote} label="/EPIGRAPH" />
          {!isComment && !isEpigraph && commandText.length > 0 && (
            <div className="px-3 py-2 text-[9px] opacity-20 italic">Plugin command or unknown command</div>
          )}
        </div>
      </div>
    </div>
  );
});

function CommandRow({ active, icon: Icon, label }: {
  active: boolean;
  icon: React.ComponentType<{ className?: string }>;
  label: string;
}) {
  return (
    <div className={cn(
      'flex items-center gap-3 px-3 py-2 rounded-lg transition-all',
      active ? 'bg-[#F1EDE4] text-black shadow-lg scale-[1.02]' : 'opacity-30',
    )}>
      <Icon className="w-3.5 h-3.5" />
      <span className="text-[10px] uppercase font-bold tracking-widest">{label}</span>
    </div>
  );
}

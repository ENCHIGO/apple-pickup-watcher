import { useEffect, useRef, useState } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";

import { cn } from "@/lib/utils";

/** 日志行。时间戳弱化，到货那几行用有货色标出来，一屏灰字里一眼能找到。 */
function LogLine({ line, className }: { line: string; className?: string }) {
  const match = /^\[(\d{2}:\d{2}:\d{2})\] ?(.*)$/s.exec(line);
  if (!match) return <div className={cn("whitespace-pre-wrap", className)}>{line}</div>;
  const [, stamp, text = ""] = match;
  return (
    <div className={cn("whitespace-pre-wrap", className)}>
      <span className="text-muted-foreground/60">{stamp}</span>{" "}
      <span className={text.startsWith("有货") ? "text-in-stock font-medium" : "text-muted-foreground"}>
        {text}
      </span>
    </div>
  );
}

/**
 * 窗口底部的日志条。平时只露最新一条，证明程序还在动；排查问题时再展开。
 *
 * 展开后停在最新一条，新日志进来跟着往下走；用户往上翻着看时不打扰。
 */
export function LogFooter({ logs }: { logs: string[] }) {
  const [open, setOpen] = useState(false);
  const viewport = useRef<HTMLDivElement>(null);
  const following = useRef(true);

  useEffect(() => {
    const el = viewport.current;
    if (open && el !== null && following.current) el.scrollTop = el.scrollHeight;
  }, [open, logs]);

  const last = logs[logs.length - 1];
  return (
    <footer className="shrink-0 border-t">
      {open && (
        <div
          ref={viewport}
          onScroll={(e) => {
            const el = e.currentTarget;
            following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8;
          }}
          className="h-48 overflow-y-auto border-b px-6 py-2 font-mono text-xs leading-5 select-text"
        >
          {logs.length === 0 ? (
            <p className="text-muted-foreground">日志会显示在这里。</p>
          ) : (
            logs.map((line, i) => <LogLine key={i} line={line} />)
          )}
        </div>
      )}
      <div className="flex h-9 items-center gap-3 px-6 text-xs">
        <div className="min-w-0 flex-1 font-mono select-text">
          {!open && last !== undefined && (
            <LogLine line={last} className="truncate whitespace-nowrap" />
          )}
        </div>
        <button
          type="button"
          aria-expanded={open}
          onClick={() => {
            following.current = true;
            setOpen(!open);
          }}
          className="text-muted-foreground hover:text-foreground flex shrink-0 items-center gap-1"
        >
          日志
          {open ? <ChevronDown className="size-3.5" /> : <ChevronUp className="size-3.5" />}
        </button>
      </div>
    </footer>
  );
}

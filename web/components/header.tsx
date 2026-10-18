import type { Board } from "@/lib/engine";
import { fmtClock } from "@/lib/format";

/** 화면 ↔ 엔진 ↔ Asterisk 연결 상태를 한 단어로 */
function linkStatus(b: Board): { label: string; tone: string; pulse?: boolean } {
  if (b.link === "engine-down") return { label: "엔진 꺼짐", tone: "tone-rose" };
  if (b.link === "connecting") return { label: "엔진 연결 중", tone: "tone-gray", pulse: true };
  if (!b.asterisk) return { label: "Asterisk 재접속 중", tone: "tone-amber", pulse: true };
  if (!b.synced) return { label: "재동기화 중", tone: "tone-amber", pulse: true };
  return { label: "실시간", tone: "tone-green" };
}

export function PageHeader({ board, now }: { board: Board; now: number }) {
  const st = linkStatus(board);
  return (
    <header className="page-header">
      <div className="min-w-0">
        <h1 className="page-title truncate">실시간 현황판</h1>
        <p className="page-sub">
          상태머신 엔진(<span className="mono">bin/engine</span>)이 낸 줄을 그대로 보여 줘요 · {board.queue} 큐
        </p>
      </div>
      <div className="pill glass" role="status" aria-live="polite">
        <span className={`dot ${st.tone} ${st.pulse ? "pulse" : ""}`} />
        <span className="font-semibold text-ink-2">{st.label}</span>
        <span className="h-4 w-px bg-[var(--line-strong)]" aria-hidden />
        <span className="badge tone-blue mono !text-[13px] tabular-nums">
          {now ? fmtClock(now + board.offsetMs) : "--:--:--"}
        </span>
      </div>
    </header>
  );
}

import type { ReactNode } from "react";
import type { Board } from "@/lib/engine";
import { fmtDuration } from "@/lib/format";

function StatCard({ label, value, unit, foot, badge }: { label: string; value: ReactNode; unit?: string; foot: ReactNode; badge?: ReactNode }) {
  return (
    <div className="stat glass">
      <div className="flex items-center justify-between gap-2">
        <span className="stat-label">{label}</span>
        {badge}
      </div>
      <div>
        <span className="stat-value">{value}</span>
        {unit && <span className="stat-unit">{unit}</span>}
      </div>
      <div className="stat-foot">{foot}</div>
    </div>
  );
}

/** 큐 현황: 대기 고객 · 통화 중 · 받을 수 있는 상담원 · 처리 결과 (엔진 시작 이후) */
export function Overview({ board, now }: { board: Board; now: number }) {
  const calls = Object.values(board.calls);
  const agents = Object.values(board.agents);
  const serverNow = now + board.offsetMs;

  const waiting = calls.filter((c) => c.state === "Queued" || c.state === "Alerting");
  const longest = waiting.reduce((m, c) => Math.max(m, serverNow - (c.queuedAt ?? c.since ?? serverNow)), 0);
  const talking = calls.filter((c) => c.state === "Connected" || c.state === "Held");
  const held = talking.filter((c) => c.state === "Held").length;

  const loggedIn = agents.filter((a) => a.state !== "LoggedOut");
  const count = (s: string) => loggedIn.filter((a) => a.state === s).length;

  const { handled, abandoned, timeout } = board.totals;
  const offered = handled + abandoned + timeout;
  const rate = offered ? Math.round((handled / offered) * 100) : null;

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <StatCard
        label="대기 고객"
        value={waiting.length}
        unit="명"
        badge={waiting.length > 0 ? <span className="badge tone-amber">대기 중</span> : undefined}
        foot={waiting.length && now ? `최장 대기 ${fmtDuration(longest)}` : "기다리는 고객이 없어요"}
      />
      <StatCard label="통화 중" value={talking.length} unit="건" foot={held ? `그중 보류 ${held}건` : "보류 없음"} />
      <StatCard
        label="받을 수 있는 상담원"
        value={count("Ready")}
        unit={`/ 로그인 ${loggedIn.length}명`}
        foot={`벨 ${count("Ringing")} · 통화 ${count("Talking") + count("Hold")} · 후처리 ${count("ACW")} · 이석 ${count("NotReady")}`}
      />
      <StatCard
        label="처리한 콜"
        value={handled}
        unit="건"
        badge={rate !== null ? <span className={`badge ${rate >= 80 ? "tone-green" : "tone-orange"}`}>응대율 {rate}%</span> : undefined}
        foot={`포기 ${abandoned} · 타임아웃 ${timeout} (엔진 시작 이후)`}
      />
    </div>
  );
}

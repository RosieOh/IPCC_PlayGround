import type { ReactNode } from "react";
import { fmtClock, fmtDuration, fmtPhone, fmtSeconds } from "@/lib/format";
import { modeInfo, phaseInfo, roomStateInfo, shortRoom, type Mode, type Phase, type RoomBoard } from "@/lib/rq";

const agentName = (b: RoomBoard, iface: string) => (iface ? b.agents[iface]?.name || iface : "—");

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

/** 큐 현황: 대기 고객(큐별) · 통화 중인 방 · 받을 수 있는 상담원 · 처리 결과 (엔진 시작 이후) */
export function RoomOverview({ board, now }: { board: RoomBoard; now: number }) {
  const rooms = Object.values(board.rooms);
  const agents = Object.values(board.agents).filter((a) => a.mode !== "Logout");
  const serverNow = now + board.offsetMs;

  const waiting = rooms.filter((r) => r.state === "Waiting" || r.state === "Offering");
  const longest = waiting.reduce((m, r) => Math.max(m, serverNow - (r.entered ?? serverNow)), 0);
  const byQueue = new Map<string, number>();
  for (const r of waiting) byQueue.set(r.queue, (byQueue.get(r.queue) ?? 0) + 1);
  const talking = rooms.filter((r) => r.state === "Talking");

  const { handled, abandoned } = board.totals;
  const offered = handled + abandoned;
  const rate = offered ? Math.round((handled / offered) * 100) : null;
  const ready = agents.filter((a) => a.mode === "Inbound" && a.phase === "Idle").length;

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <StatCard
        label="대기 고객"
        value={waiting.length}
        unit="명"
        badge={waiting.length > 0 ? <span className="badge tone-amber">대기 중</span> : undefined}
        foot={
          waiting.length && now
            ? `${[...byQueue].map(([q, n]) => `${q} ${n}`).join(" · ")} · 최장 ${fmtDuration(longest)}`
            : "기다리는 고객이 없어요"
        }
      />
      <StatCard
        label="통화 중인 방"
        value={talking.length}
        unit="개"
        foot={`보류 ${talking.filter((r) => r.hold).length} · 협의 ${talking.filter((r) => r.consult).length} · 3자 ${talking.filter((r) => r.agents.length > 1 && !r.consult).length}`}
      />
      <StatCard
        label="받을 수 있는 상담원"
        value={ready}
        unit={`/ 로그인 ${agents.length}명`}
        foot="업무 모드가 대기이고 통화가 없는 상담원 (아래 표의 초록 칸)"
      />
      <StatCard
        label="처리한 콜"
        value={handled}
        unit="건"
        badge={rate !== null ? <span className={`badge ${rate >= 80 ? "tone-green" : "tone-orange"}`}>응대율 {rate}%</span> : undefined}
        foot={`포기 ${abandoned} (엔진 시작 이후)`}
      />
    </div>
  );
}

const MODES: Mode[] = ["Inbound", "ACW", "NotReady"];
const PHASES: Phase[] = ["Idle", "Ring", "Talk"];

/**
 * 두 축 표: 업무 모드(행) × 통화 단계(열)마다 상담원 수.
 * 한 축 모델이라면 "이석 예약" 같은 상태를 따로 만들어야 할 칸(이석 × 통화)도 여기서는 그냥 한 칸이다.
 */
export function ModePhaseMatrix({ board }: { board: RoomBoard }) {
  const agents = Object.values(board.agents);
  const cell = (m: Mode, p: Phase) => agents.filter((a) => a.mode === m && a.phase === p);
  return (
    <div className="table-wrap glass">
      <table className="table">
        <thead>
          <tr>
            <th>업무 모드 ＼ 통화 단계</th>
            {PHASES.map((p) => (
              <th key={p} className="text-center">
                <span className={`badge tone-${phaseInfo[p].tone}`}>{phaseInfo[p].label}</span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {MODES.map((m) => (
            <tr key={m}>
              <td>
                <span className={`badge tone-${modeInfo[m].tone}`}>{modeInfo[m].label}</span>
              </td>
              {PHASES.map((p) => {
                const list = cell(m, p);
                const dispatchable = m === "Inbound" && p === "Idle";
                return (
                  <td key={p} className={`text-center ${dispatchable ? "tone-green" : ""}`} title={list.map((a) => a.name || a.id).join(", ")}>
                    <span className={`td-num text-[18px] ${list.length ? "td-strong" : "text-ink-4"}`}>{list.length}</span>
                    {dispatchable && <div className="caption !text-[var(--tone-green)]">콜을 받는 칸</div>}
                    {list.length > 0 && !dispatchable && (
                      <div className="caption truncate">{list.map((a) => a.name || a.id).join(", ")}</div>
                    )}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** 진행 중인 방: 먼저 들어온 고객부터. 대기 중이면 들어온 뒤로, 통화 중이면 지금 상태가 된 뒤로 센다 */
export function LiveRooms({ board, now }: { board: RoomBoard; now: number }) {
  const serverNow = now + board.offsetMs;
  const rooms = Object.values(board.rooms).sort((a, b) => (a.entered ?? 0) - (b.entered ?? 0));
  return (
    <div className="table-wrap glass">
      <table className="table">
        <thead>
          <tr>
            <th>고객 · 방</th>
            <th>큐</th>
            <th>상태</th>
            <th>방 안의 상담원</th>
            <th className="text-right">경과</th>
          </tr>
        </thead>
        <tbody>
          {rooms.length === 0 ? (
            <tr>
              <td colSpan={5} className="empty">
                진행 중인 방이 없어요. <span className="kbd">make sim-rq-call n=3</span>
              </td>
            </tr>
          ) : (
            rooms.map((r) => {
              const info = roomStateInfo[r.state];
              const from = r.state === "Talking" ? r.since : r.entered;
              return (
                <tr key={r.id}>
                  <td>
                    <div className="td-strong td-num">{r.customer ? fmtPhone(r.customer) : "—"}</div>
                    <div className="mono" title={r.id}>
                      {shortRoom(r.id)}
                    </div>
                  </td>
                  <td>
                    <span className="badge tone-gray">{r.queue || "—"}</span>
                  </td>
                  <td>
                    <div className="flex flex-wrap gap-1">
                      <span className={`badge tone-${info.tone}`}>{info.label}</span>
                      {r.hold && <span className="badge tone-orange">고객 보류</span>}
                      {r.consult && <span className="badge tone-violet">협의 → {agentName(board, r.consult)}</span>}
                    </div>
                  </td>
                  <td>{r.agents.length ? r.agents.map((a) => agentName(board, a)).join(", ") : "—"}</td>
                  <td className="td-num text-right">{from && now ? fmtDuration(serverNow - from) : "—"}</td>
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
}

/** 최근 종료한 콜과 결과 (call_end 줄) */
export function EndedRooms({ board }: { board: RoomBoard }) {
  return (
    <div className="table-wrap glass">
      <table className="table">
        <thead>
          <tr>
            <th>종료</th>
            <th>고객</th>
            <th>결과</th>
            <th className="text-right">대기</th>
            <th className="text-right">통화</th>
            <th>받은 상담원</th>
          </tr>
        </thead>
        <tbody>
          {board.ended.length === 0 ? (
            <tr>
              <td colSpan={6} className="empty">
                아직 끝난 콜이 없어요
              </td>
            </tr>
          ) : (
            board.ended.map((e) => (
              <tr key={`${e.id}-${e.t}`}>
                <td className="td-num">{fmtClock(parseFloat(e.t) * 1000)}</td>
                <td className="td-strong td-num">{e.customer ? fmtPhone(e.customer) : "—"}</td>
                <td>
                  <span className={`badge ${e.outcome === "completed" ? "tone-green" : "tone-rose"}`}>
                    {e.outcome === "completed" ? "응대" : "포기"}
                  </span>
                  {e.queue && <span className="badge tone-gray ml-1">{e.queue}</span>}
                </td>
                <td className="td-num text-right">{fmtSeconds(e.wait_ms)}</td>
                <td className="td-num text-right">{e.talk_ms ? fmtSeconds(e.talk_ms) : "—"}</td>
                <td>{agentName(board, e.agents ?? "")}</td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

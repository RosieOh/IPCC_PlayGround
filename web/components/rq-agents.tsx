"use client";

import { useState } from "react";
import { fmtDuration, fmtPhone } from "@/lib/format";
import { modeInfo, phaseInfo, SKILL_PRESETS, skillChips, type Agent, type RoomBoard } from "@/lib/rq";
import { sendCommand } from "@/lib/use-engine";
import { ReasonMenu } from "./agents";
import { IconLogout } from "./icons";
import { PickMenu } from "./menu";

export const RQ_COMMANDS = "/api/rq/commands";

const PHASE_ORDER = ["Ring", "Talk", "Idle"];
const MODE_ORDER = ["Inbound", "ACW", "NotReady", "Logout"];

export function RoomAgents({ board, now }: { board: RoomBoard; now: number }) {
  const agents = Object.values(board.agents).sort(
    (a, b) =>
      PHASE_ORDER.indexOf(a.phase) - PHASE_ORDER.indexOf(b.phase) ||
      MODE_ORDER.indexOf(a.mode) - MODE_ORDER.indexOf(b.mode) ||
      (a.name || a.id).localeCompare(b.name || b.id),
  );
  if (agents.length === 0) {
    return (
      <div className="glass rounded-[22px]">
        <p className="empty">
          엔진이 아는 상담원이 없어요. 위에서 <span className="mono">Local/1001@sim-agent</span>처럼 로그인하고 [대기]를 눌러 보세요.
        </p>
      </div>
    );
  }
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
      {agents.map((a) => (
        <RoomAgentCard key={a.id} agent={a} board={board} now={now} />
      ))}
    </div>
  );
}

/** 상담원 카드: 업무 모드와 통화 단계를 배지·타이머 두 쌍으로 따로 보여 준다 */
function RoomAgentCard({ agent, board, now }: { agent: Agent; board: RoomBoard; now: number }) {
  const mode = modeInfo[agent.mode];
  const phase = phaseInfo[agent.phase];
  const serverNow = now + board.offsetMs;
  const since = (t?: number) => (t && now ? serverNow - t : undefined);
  const modeElapsed = since(agent.modeSince);
  const phaseElapsed = since(agent.phaseSince);
  const modeTimer =
    modeElapsed === undefined ? "—" : agent.mode === "ACW" ? fmtDuration(Math.max(0, board.acwMs - modeElapsed)) : fmtDuration(modeElapsed);
  const room = agent.room ? board.rooms[agent.room] : undefined;
  const customer = agent.room ? board.customers[agent.room] : undefined;
  const talking = agent.phase === "Talk" && room !== undefined;
  // 협의 중이면 방에는 원래 상담원과 넘겨받을 상담원이 있다. 완료·취소는 원래 상담원이 한다
  const consulting = talking && room.consult !== "" && room.consult !== agent.id;
  const isConsultTarget = talking && room.consult === agent.id;
  const targets = Object.values(board.agents)
    .filter((o) => o.id !== agent.id && o.mode !== "Logout" && o.phase === "Idle")
    .map((o) => ({ value: o.id, label: o.name || o.id, hint: `${modeInfo[o.mode].label}` }));

  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async (kind: string, arg = "") => {
    setBusy(true);
    setError(null);
    try {
      await sendCommand(kind, agent.id, arg, RQ_COMMANDS);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const chips = skillChips(agent.skills);
  return (
    <article className={`agent glass tone-${agent.phase === "Idle" ? mode.tone : phase.tone} ${agent.mode === "Logout" ? "opacity-60" : ""}`}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className={`dot ${agent.phase === "Ring" ? "pulse" : ""}`} />
            <h3 className="truncate text-[17px] font-semibold tracking-[-0.03em]">{agent.name || agent.id}</h3>
          </div>
          <p className="mono mt-0.5 truncate">{agent.id}</p>
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1">
          <span className={`badge tone-${mode.tone}`} title="업무 모드">
            {mode.label}
            {agent.mode === "NotReady" && agent.reason ? ` · ${agent.reason}` : ""}
          </span>
          <span className={`badge tone-${phase.tone}`} title="통화 단계">
            {phase.label}
          </span>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <div className="caption">업무 모드 · {agent.mode === "ACW" ? "남은 시간" : mode.label}</div>
          <div className="timer !text-[26px]">{modeTimer}</div>
        </div>
        <div>
          <div className="caption">통화 단계 · {phase.timer}</div>
          <div className="timer !text-[26px]">{phaseElapsed === undefined ? "—" : fmtDuration(phaseElapsed)}</div>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        {chips.length === 0 ? (
          <span className="badge tone-gray">모든 큐</span>
        ) : (
          chips.map((c) => (
            <span key={c} className="badge tone-blue">
              {c}
            </span>
          ))
        )}
        {customer && agent.phase !== "Idle" && (
          <span className="ml-auto text-right">
            <span className="text-[15px] font-semibold tabular-nums">{fmtPhone(customer)}</span>
            {room?.hold && <span className="badge tone-orange ml-1.5">보류</span>}
            {room?.consult && <span className="badge tone-violet ml-1.5">협의</span>}
          </span>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t border-[var(--line)] pt-3">
        {agent.mode === "Logout" && (
          <button className="btn btn-xs btn-primary" disabled={busy} onClick={() => run("login", agent.name)}>
            다시 로그인
          </button>
        )}
        {agent.mode === "NotReady" && (
          <button className="btn btn-xs btn-primary" disabled={busy} onClick={() => run("ready")}>
            대기
          </button>
        )}
        {agent.mode === "ACW" && (
          <button className="btn btn-xs btn-primary" disabled={busy} onClick={() => run("wrapup")}>
            후처리 완료
          </button>
        )}
        {(agent.mode === "Inbound" || agent.mode === "ACW") && <ReasonMenu label="이석" disabled={busy} onPick={(r) => run("notready", r)} />}
        {agent.mode !== "Logout" && (
          <PickMenu label="스킬" disabled={busy} options={SKILL_PRESETS} onPick={(s) => run("skills", s)} />
        )}
        {agent.mode !== "Logout" && (
          <button className="icon-btn ml-auto" disabled={busy} onClick={() => run("logout")} aria-label={`${agent.name} 로그아웃`} title="로그아웃">
            <IconLogout className="size-[18px]" />
          </button>
        )}
      </div>

      {talking && !isConsultTarget && (
        <div className="-mt-1 flex flex-wrap items-center gap-2">
          {consulting ? (
            <>
              <button className="btn btn-xs btn-primary" disabled={busy} onClick={() => run("complete")} title="내가 빠지고 고객을 넘긴다">
                협의 완료 (넘기기)
              </button>
              <button className="btn btn-xs btn-secondary" disabled={busy} onClick={() => run("cancel")} title="넘겨받을 상담원을 빼고 고객을 돌려받는다">
                협의 취소
              </button>
              <button className="btn btn-xs btn-secondary" disabled={busy} onClick={() => run("unhold")} title="고객을 돌려보내 셋이 통화한다">
                3자로 합치기
              </button>
            </>
          ) : (
            <>
              <button className="btn btn-xs btn-secondary" disabled={busy} onClick={() => run(room.hold ? "unhold" : "hold")}>
                {room.hold ? "보류 해제" : "보류"}
              </button>
              <PickMenu label="전환" disabled={busy} options={targets} onPick={(t) => run("transfer", t)} empty="부를 수 있는 상담원이 없어요" />
              <PickMenu label="협의" disabled={busy} options={targets} onPick={(t) => run("consult", t)} empty="부를 수 있는 상담원이 없어요" />
              <PickMenu label="3자" disabled={busy} options={targets} onPick={(t) => run("conference", t)} empty="부를 수 있는 상담원이 없어요" />
            </>
          )}
        </div>
      )}
      {agent.phase === "Talk" && agent.mode === "NotReady" && !isConsultTarget && (
        <p className="caption -mt-1">두 축이라 통화는 그대로예요. 통화가 끝나면 후처리 없이 이석에 머물러요.</p>
      )}
      {isConsultTarget && <p className="caption -mt-1">협의 중: 고객은 보류 방에 있어요. 원래 상담원이 넘기면 고객과 이어져요.</p>}
      {error && <p className="caption -mt-1 !text-[var(--tone-rose)]">{error}</p>}
    </article>
  );
}

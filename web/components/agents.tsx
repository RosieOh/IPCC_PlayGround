"use client";

import { useState, type FormEvent } from "react";
import { agentStateInfo, NOT_READY_REASONS, type Agent, type Board } from "@/lib/engine";
import { fmtDuration, fmtPhone } from "@/lib/format";
import { sendCommand } from "@/lib/use-engine";
import { IconLogout } from "./icons";
import { PickMenu } from "./menu";

const ORDER = ["Ringing", "Talking", "Hold", "ACW", "Ready", "NotReady", "LoggedOut"];

export function AgentBoard({ board, now }: { board: Board; now: number }) {
  const agents = Object.values(board.agents).sort(
    (a, b) => ORDER.indexOf(a.state) - ORDER.indexOf(b.state) || a.name.localeCompare(b.name) || a.id.localeCompare(b.id),
  );
  if (agents.length === 0) {
    return (
      <div className="glass rounded-[22px]">
        <p className="empty">
          엔진이 아는 상담원이 없어요. 위에서 로그인하거나 <span className="kbd">make sim-agents</span>로 가짜 상담원을 넣어 보세요.
        </p>
      </div>
    );
  }
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
      {agents.map((a) => (
        <AgentCard key={a.id} agent={a} board={board} now={now} />
      ))}
    </div>
  );
}

function AgentCard({ agent, board, now }: { agent: Agent; board: Board; now: number }) {
  const info = agentStateInfo[agent.state];
  const serverNow = now + board.offsetMs;
  const elapsed = agent.since && now ? serverNow - agent.since : undefined;
  const customer = agent.call ? board.customers[agent.call] : undefined;
  const inCall = agent.state === "Ringing" || agent.state === "Talking" || agent.state === "Hold";

  let timer = elapsed !== undefined ? fmtDuration(elapsed) : "—";
  if (agent.state === "ACW" && elapsed !== undefined) timer = fmtDuration(Math.max(0, board.acwMs - elapsed));

  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async (kind: string, arg = "") => {
    setBusy(true);
    setError(null);
    try {
      await sendCommand(kind, agent.id, arg);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <article className={`agent glass tone-${info.tone} ${agent.state === "LoggedOut" ? "opacity-60" : ""}`}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className={`dot ${agent.state === "Ringing" ? "pulse" : ""}`} />
            <h3 className="truncate text-[17px] font-semibold tracking-[-0.03em]">{agent.name || agent.id}</h3>
          </div>
          <p className="mono mt-0.5 truncate">{agent.id}</p>
        </div>
        <span className="badge shrink-0">
          {info.label}
          {agent.state === "NotReady" && agent.reason ? ` · ${agent.reason}` : ""}
        </span>
      </div>

      <div className="flex items-end justify-between gap-3">
        <div>
          <div className="timer">{timer}</div>
          <div className="caption mt-1">{info.timer}</div>
        </div>
        {customer && agent.state !== "Ready" && agent.state !== "NotReady" && agent.state !== "LoggedOut" && (
          <div className="text-right">
            <div className="text-[15px] font-semibold tabular-nums">{fmtPhone(customer)}</div>
            <div className="caption">{agent.state === "ACW" ? "방금 끝난 고객" : "고객"}</div>
          </div>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t border-[var(--line)] pt-3">
        {agent.state === "LoggedOut" && (
          <button className="btn btn-xs btn-primary" disabled={busy} onClick={() => run("login", agent.name)}>
            다시 로그인
          </button>
        )}
        {agent.state === "NotReady" && (
          <button className="btn btn-xs btn-primary" disabled={busy} onClick={() => run("ready")}>
            대기
          </button>
        )}
        {agent.state === "ACW" && (
          <button className="btn btn-xs btn-primary" disabled={busy} onClick={() => run("wrapup")}>
            후처리 완료
          </button>
        )}
        {(agent.state === "Ready" || agent.state === "ACW" || inCall) && (
          <ReasonMenu label={inCall ? "이석 예약" : "이석"} disabled={busy} onPick={(r) => run("notready", r)} />
        )}
        {agent.state !== "LoggedOut" && (
          <button
            className="icon-btn ml-auto"
            disabled={busy}
            onClick={() => run("logout")}
            aria-label={`${agent.name} 로그아웃`}
            title="로그아웃"
          >
            <IconLogout className="size-[18px]" />
          </button>
        )}
      </div>
      {inCall && <p className="caption -mt-1">통화 중에 누른 이석은 예약돼요. 후처리가 끝나면 이석으로 바뀌어요.</p>}
      {error && <p className="caption -mt-1 !text-[var(--tone-rose)]">{error}</p>}
    </article>
  );
}

/** 이석 사유를 고르는 작은 메뉴 */
export function ReasonMenu({ label, disabled, onPick }: { label: string; disabled: boolean; onPick: (reason: string) => void }) {
  return <PickMenu label={label} disabled={disabled} onPick={onPick} options={NOT_READY_REASONS.map((r) => ({ value: r, label: r }))} />;
}

const PRESETS = [
  "Local/1001@sim-agent",
  "Local/1002@sim-agent",
  "Local/1003@sim-agent",
  "PJSIP/1001",
  "PJSIP/1002",
  "PJSIP/1003",
];

/** 인터페이스로 기본 이름을 정한다: Local/1001@sim-agent → SIM-1001, PJSIP/1001 → 1001 */
function defaultName(iface: string): string {
  const sim = /^Local\/(\d+)@sim-agent$/.exec(iface);
  if (sim) return `SIM-${sim[1]}`;
  return iface.split("/").pop() ?? iface;
}

/** 상담원 로그인. 엔진은 NotReady(로그인)로 둔다 → 카드의 [대기]를 눌러야 콜을 받는다. endpoint는 명령 게이트웨이 */
export function LoginForm({ endpoint = "/api/engine/commands" }: { endpoint?: string }) {
  const listId = `agent-presets${endpoint.replace(/\W+/g, "-")}`;
  const [iface, setIface] = useState(PRESETS[0]);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await sendCommand("login", iface.trim(), name.trim() || defaultName(iface.trim()), endpoint);
      setName("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
      <input
        className="input w-[13.5rem]"
        list={listId}
        value={iface}
        onChange={(e) => setIface(e.target.value)}
        aria-label="상담원 인터페이스"
        placeholder="Local/1001@sim-agent"
        required
      />
      <datalist id={listId}>
        {PRESETS.map((p) => (
          <option key={p} value={p} />
        ))}
      </datalist>
      <input
        className="input w-[8.5rem]"
        value={name}
        onChange={(e) => setName(e.target.value)}
        aria-label="상담원 이름"
        placeholder={defaultName(iface)}
      />
      <button className="btn btn-primary btn-xs !py-[0.4375rem]" disabled={busy}>
        로그인
      </button>
      {error && <span className="caption !text-[var(--tone-rose)]">{error}</span>}
    </form>
  );
}

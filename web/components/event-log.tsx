"use client";

import { useState, type ReactNode } from "react";
import { agentStateInfo, callStateInfo, outcomeInfo, warnLabel, type Board, type Line } from "@/lib/engine";
import { fmtClock, fmtPhone, fmtSeconds, shortId } from "@/lib/format";

type Filter = "all" | "agent" | "call" | "action" | "warn";

const FILTERS: { id: Filter; label: string }[] = [
  { id: "all", label: "전체" },
  { id: "agent", label: "상담원" },
  { id: "call", label: "콜" },
  { id: "action", label: "Asterisk 액션" },
  { id: "warn", label: "경고·연결" },
];

const KIND: Record<Line["kind"], { label: string; tone: string }> = {
  agent: { label: "상담원", tone: "tone-blue" },
  call: { label: "콜", tone: "tone-gray" },
  call_end: { label: "결과", tone: "tone-green" },
  action: { label: "액션", tone: "tone-violet" },
  warn: { label: "경고", tone: "tone-amber" },
  status: { label: "연결", tone: "tone-gray" },
};

function matches(f: Filter, l: Line): boolean {
  switch (f) {
    case "all":
      return true;
    case "agent":
      return l.kind === "agent";
    case "call":
      return l.kind === "call" || l.kind === "call_end";
    case "action":
      return l.kind === "action";
    case "warn":
      return l.kind === "warn" || l.kind === "status";
  }
}

/** 상태머신 출력 한 줄을 사람이 읽는 문장으로 */
function describe(l: Line, b: Board): ReactNode {
  const who = (iface: string) => b.agents[iface]?.name ?? iface;
  const customer = (id: string, n?: string) => (n ? fmtPhone(n) : b.customers[id] ? fmtPhone(b.customers[id]) : shortId(id));
  switch (l.kind) {
    case "agent":
      return (
        <>
          <b>{l.name || l.id}</b> {agentStateInfo[l.from]?.label ?? l.from} → <b>{agentStateInfo[l.to]?.label ?? l.to}</b>
          {l.reason ? ` (${l.reason})` : ""}
        </>
      );
    case "call":
      return (
        <>
          콜 <b>{customer(l.id, l.customer)}</b> {l.from ? `${callStateInfo[l.from]?.label ?? l.from} → ` : "생김 → "}
          <b>{callStateInfo[l.to]?.label ?? l.to}</b>
          {l.agent ? ` · ${who(l.agent)}` : ""}
        </>
      );
    case "call_end":
      return (
        <>
          콜 <b>{customer(l.id, l.customer)}</b> 종료 — <b>{outcomeInfo(l.outcome).label}</b> · 대기 {fmtSeconds(l.wait_ms)}
          {l.talk_ms ? ` · 통화 ${fmtSeconds(l.talk_ms)}` : ""}
        </>
      );
    case "action": {
      const args =
        l.action === "QueueRemove" ? "" : `(${l.paused ? "이석" : "대기"}${l.reason ? `, ${l.reason}` : ""})`;
      return (
        <>
          Asterisk ← <b>{l.action}</b>
          {args} {who(l.iface)}
        </>
      );
    }
    case "warn":
      return (
        <>
          <b>{warnLabel[l.code] ?? l.code}</b> <span className="mono">{l.id}</span>
        </>
      );
    case "status":
      return <b>{l.connected ? (l.synced ? "AMI 연결 · 재동기화 끝" : "AMI 연결됨 (부팅 완료를 기다리는 중)") : "AMI 연결 끊김"}</b>;
  }
}

export function EventLog({ board }: { board: Board }) {
  const [filter, setFilter] = useState<Filter>("all");
  const rows = board.log.filter((e) => matches(filter, e.line));
  const newest = board.log[0]?.seq ?? 0;

  return (
    <div className="glass rounded-[22px] p-3">
      <div className="flex flex-wrap items-center justify-between gap-3 px-2 pb-2 pt-1">
        <div className="seg" role="group" aria-label="줄 종류">
          {FILTERS.map((f) => (
            <button key={f.id} type="button" className="seg-item" aria-pressed={filter === f.id} onClick={() => setFilter(f.id)}>
              {f.label}
            </button>
          ))}
        </div>
        <span className="caption">최근 {board.log.length}줄 · 새 줄이 위에 쌓여요</span>
      </div>
      <div className="log">
        {rows.length === 0 ? (
          <p className="empty">아직 줄이 없어요</p>
        ) : (
          rows.map(({ seq, line }) => {
            const k = KIND[line.kind];
            return (
              <div key={seq} className={`log-row ${seq > newest - 3 ? "is-new" : ""}`}>
                <span className="mono">{fmtClock(parseFloat(line.t) * 1000, true)}</span>
                <span>
                  <span className={`badge ${k.tone}`}>{k.label}</span>
                </span>
                <span className="log-msg">{describe(line, board)}</span>
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}

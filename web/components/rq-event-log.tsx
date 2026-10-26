"use client";

import { useState, type ReactNode } from "react";
import { fmtClock, fmtPhone, fmtSeconds } from "@/lib/format";
import { modeInfo, phaseInfo, roomStateInfo, shortRoom, warnLabel, type Line, type RoomBoard } from "@/lib/rq";

type Filter = "all" | "agent" | "call" | "action" | "warn";

const FILTERS: { id: Filter; label: string }[] = [
  { id: "all", label: "전체" },
  { id: "agent", label: "상담원" },
  { id: "call", label: "방·콜" },
  { id: "action", label: "Asterisk 액션" },
  { id: "warn", label: "경고·연결" },
];

const KIND: Record<Line["kind"], { label: string; tone: string }> = {
  agent: { label: "상담원", tone: "tone-blue" },
  call: { label: "방", tone: "tone-gray" },
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

/** 엔진 출력 한 줄을 사람이 읽는 문장으로. 액션은 엔진이 Asterisk에 보낸 AMI 액션 이름으로 적는다 */
function describe(l: Line, b: RoomBoard): ReactNode {
  const who = (iface?: string) => (iface ? b.agents[iface]?.name || iface : "—");
  const customer = (room: string, n?: string) => (n ? fmtPhone(n) : b.customers[room] ? fmtPhone(b.customers[room]) : shortRoom(room));
  switch (l.kind) {
    case "agent":
      return (
        <>
          <b>{l.name || l.id}</b> 업무 <b>{modeInfo[l.mode]?.label ?? l.mode}</b>
          {l.reason ? ` (${l.reason})` : ""} · 통화 <b>{phaseInfo[l.phase]?.label ?? l.phase}</b>
          {l.room ? <span className="mono"> {shortRoom(l.room)}</span> : ""}
        </>
      );
    case "call":
      return (
        <>
          방 <b>{customer(l.id, l.customer)}</b> <b>{roomStateInfo[l.state]?.label ?? l.state}</b>
          {l.hold ? " · 고객 보류" : ""}
          {l.consult ? ` · 협의 → ${who(l.consult)}` : ""}
          {l.agents ? ` · ${l.agents.split(",").map(who).join(", ")}` : ""}
        </>
      );
    case "call_end":
      return (
        <>
          콜 <b>{customer(l.id, l.customer)}</b> 종료 — <b>{l.outcome === "completed" ? "응대" : "포기"}</b> · 대기 {fmtSeconds(l.wait_ms)}
          {l.talk_ms ? ` · 통화 ${fmtSeconds(l.talk_ms)}` : ""}
        </>
      );
    case "action":
      switch (l.action) {
        case "Originate":
          return (
            <>
              Asterisk ← <b>Originate</b> {who(l.iface)}을(를) 방 <span className="mono">{shortRoom(l.room ?? "")}</span>으로
            </>
          );
        case "Hangup":
          return (
            <>
              Asterisk ← <b>Hangup</b> {who(l.iface)}
              {l.channel && l.room && l.channel.startsWith(`${l.room}-a`) ? " (벨 끊기)" : ""}
            </>
          );
        case "Hold":
          return (
            <>
              Asterisk ← <b>Redirect</b> 고객 → 보류 방
            </>
          );
        case "Unhold":
          return (
            <>
              Asterisk ← <b>Redirect</b> 고객 → 원래 방
            </>
          );
        case "Record":
          return (
            <>
              Asterisk ← <b>ConfbridgeStartRecord</b> <span className="mono">{shortRoom(l.room ?? "")}</span>
            </>
          );
      }
      return <b>{l.action}</b>;
    case "warn":
      return (
        <>
          <b>{warnLabel[l.code] ?? l.code}</b> <span className="mono">{l.id}</span>
        </>
      );
    case "status":
      return <b>{l.connected ? (l.synced ? "AMI 연결 · 채널 목록으로 재동기화 끝" : "AMI 연결됨 (부팅 완료를 기다리는 중)") : "AMI 연결 끊김"}</b>;
  }
}

export function RoomEventLog({ board }: { board: RoomBoard }) {
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

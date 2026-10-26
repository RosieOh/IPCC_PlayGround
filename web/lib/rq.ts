// 룸 큐 엔진(bin/rqengine)이 SSE로 보내는 줄의 타입과, 그 줄을 화면 상태로 접는 리듀서.
//
// 줄 형식: engine/rq/rq.go의 Line (빈 값은 생략된다), 접속 시 전체 상태(state)와 연결 상태(status)는 engine/rq/view.go.
// 상담원 상태는 두 축이다: 업무 모드(mode)와 통화 단계(phase). 화면도 두 축을 따로 보여 준다 (docs/12).

import type { Link, Tone } from "./engine";
import { toMs } from "./engine";
import type { StreamAction } from "./use-engine";

export type Mode = "Logout" | "Inbound" | "NotReady" | "ACW";
export type Phase = "Idle" | "Ring" | "Talk";
export type RoomState = "Waiting" | "Offering" | "Talking" | "Ended";

export type AgentLine = {
  t: string;
  kind: "agent";
  id: string;
  name?: string;
  mode: Mode;
  phase: Phase;
  reason?: string;
  skills?: string; // "sales:1,support:2" (없으면 모든 큐)
  room?: string;
};
export type CallLine = {
  t: string;
  kind: "call";
  id: string; // 방 rq-<큐>-<Linkedid>
  state: RoomState;
  hold?: boolean;
  consult?: string; // 협의 중이면 넘겨받을 상담원
  queue?: string;
  customer?: string;
  entered?: string;
  agents?: string; // 방에 있는 상담원 (쉼표)
};
export type CallEndLine = {
  t: string;
  kind: "call_end";
  id: string;
  queue?: string;
  customer?: string;
  agents?: string; // 처음 받은 상담원
  outcome: "completed" | "abandoned";
  wait_ms: number;
  talk_ms: number;
};
export type ActionLine = {
  t: string;
  kind: "action";
  action: "Originate" | "Hangup" | "Hold" | "Unhold" | "Record";
  iface?: string;
  room?: string;
  channel?: string;
  customer?: string;
};
export type WarnLine = { t: string; kind: "warn"; code: string; id?: string };
export type StatusLine = { t: string; kind: "status"; connected: boolean; synced: boolean };
export type Line = AgentLine | CallLine | CallEndLine | ActionLine | WarnLine | StatusLine;

export type Totals = { handled: number; abandoned: number };

export type StateView = {
  kind: "state";
  now: string;
  connected: boolean;
  synced: boolean;
  policy: { acw_ms: number; ring_ms: number; max_missed: number; record: boolean };
  agents: AgentLine[];
  calls: CallLine[];
  since: { mode: Record<string, string>; phase: Record<string, string>; calls: Record<string, string> };
  totals: Totals;
  recent: Line[];
};

export type Agent = {
  id: string;
  name: string;
  mode: Mode;
  phase: Phase;
  reason: string;
  skills: string;
  room: string;
  modeSince?: number; // 지금 업무 모드(사유 포함)가 된 시각 (ms)
  phaseSince?: number; // 지금 통화 단계가 된 시각 (ms)
};
export type Room = {
  id: string;
  state: RoomState;
  hold: boolean;
  consult: string;
  queue: string;
  customer: string;
  agents: string[];
  entered?: number; // 고객이 방에 들어온 시각 (ms)
  since?: number; // 지금 상태가 된 시각 (ms)
};
export type LogEntry = { seq: number; line: Line };

export type RoomBoard = {
  link: Link;
  asterisk: boolean;
  synced: boolean;
  acwMs: number;
  ringMs: number;
  record: boolean;
  offsetMs: number; // 엔진 시계 − 브라우저 시계
  agents: Record<string, Agent>;
  rooms: Record<string, Room>;
  customers: Record<string, string>; // 방 → 고객 번호 (끝난 방도 후처리 중에 보여 주려고 남긴다)
  ended: CallEndLine[]; // 최근 종료, 새것이 앞
  log: LogEntry[]; // 새것이 앞
  totals: Totals;
  seq: number;
};

const LOG_MAX = 200;
const ENDED_MAX = 12;

export const initialRoomBoard: RoomBoard = {
  link: "connecting",
  asterisk: false,
  synced: false,
  acwMs: 10_000,
  ringMs: 15_000,
  record: false,
  offsetMs: 0,
  agents: {},
  rooms: {},
  customers: {},
  ended: [],
  log: [],
  totals: { handled: 0, abandoned: 0 },
  seq: 0,
};

export function reduceRooms(b: RoomBoard, a: StreamAction<StateView, Line>): RoomBoard {
  switch (a.type) {
    case "link":
      if (a.link === "connecting" && b.link === "engine-down") return b;
      return { ...b, link: a.link };
    case "state":
      return fromView(a.view, a.receivedAt);
    case "line":
      return applyLine(b, a.line);
  }
}

const toAgent = (l: AgentLine, modeSince?: number, phaseSince?: number): Agent => ({
  id: l.id,
  name: l.name ?? "",
  mode: l.mode,
  phase: l.phase,
  reason: l.reason ?? "",
  skills: l.skills ?? "",
  room: l.room ?? "",
  modeSince,
  phaseSince,
});

const toRoom = (l: CallLine, since?: number): Room => ({
  id: l.id,
  state: l.state,
  hold: l.hold ?? false,
  consult: l.consult ?? "",
  queue: l.queue ?? "",
  customer: l.customer ?? "",
  agents: l.agents ? l.agents.split(",") : [],
  entered: toMs(l.entered),
  since,
});

function fromView(v: StateView, receivedAt: number): RoomBoard {
  const agents: Record<string, Agent> = {};
  for (const l of v.agents) agents[l.id] = toAgent(l, toMs(v.since.mode[l.id]), toMs(v.since.phase[l.id]));
  const rooms: Record<string, Room> = {};
  const customers: Record<string, string> = {};
  for (const l of v.calls) {
    rooms[l.id] = toRoom(l, toMs(v.since.calls[l.id]));
    if (l.customer) customers[l.id] = l.customer;
  }
  let seq = 0;
  const log: LogEntry[] = [];
  const ended: CallEndLine[] = [];
  for (const line of v.recent) {
    log.unshift({ seq: ++seq, line });
    if (line.kind === "call_end") ended.unshift(line);
    if ((line.kind === "call" || line.kind === "call_end") && line.customer) customers[line.id] = line.customer;
  }
  return {
    link: "live",
    asterisk: v.connected,
    synced: v.synced,
    acwMs: v.policy.acw_ms,
    ringMs: v.policy.ring_ms,
    record: v.policy.record,
    offsetMs: (toMs(v.now) ?? receivedAt) - receivedAt,
    agents,
    rooms,
    customers,
    ended: ended.slice(0, ENDED_MAX),
    log: log.slice(0, LOG_MAX),
    totals: v.totals,
    seq,
  };
}

function applyLine(b: RoomBoard, line: Line): RoomBoard {
  const seq = b.seq + 1;
  const next: RoomBoard = { ...b, seq, log: [{ seq, line }, ...b.log].slice(0, LOG_MAX) };
  switch (line.kind) {
    case "status":
      next.asterisk = line.connected;
      next.synced = line.synced;
      break;
    case "agent": {
      // 엔진(view.go)과 같이: 바뀐 축의 시각만 새로 잡는다
      const prev = b.agents[line.id];
      const t = toMs(line.t);
      const modeSince = !prev || prev.mode !== line.mode || prev.reason !== (line.reason ?? "") ? t : prev.modeSince;
      const phaseSince = !prev || prev.phase !== line.phase ? t : prev.phaseSince;
      next.agents = { ...b.agents, [line.id]: toAgent(line, modeSince, phaseSince) };
      break;
    }
    case "call": {
      const rooms = { ...b.rooms };
      if (line.state === "Ended") {
        delete rooms[line.id];
      } else {
        const prev = rooms[line.id];
        rooms[line.id] = toRoom(line, prev && prev.state === line.state ? prev.since : toMs(line.t));
      }
      next.rooms = rooms;
      if (line.customer) next.customers = { ...b.customers, [line.id]: line.customer };
      break;
    }
    case "call_end":
      next.ended = [line, ...b.ended].slice(0, ENDED_MAX);
      next.totals =
        line.outcome === "completed"
          ? { ...b.totals, handled: b.totals.handled + 1 }
          : { ...b.totals, abandoned: b.totals.abandoned + 1 };
      if (line.customer) next.customers = { ...b.customers, [line.id]: line.customer };
      break;
    case "action":
    case "warn":
      break;
  }
  return next;
}

// ── 표시용 이름 ──────────────────────────────────────────────────────────

export const modeInfo: Record<Mode, { label: string; tone: Tone }> = {
  Inbound: { label: "대기", tone: "green" },
  NotReady: { label: "이석", tone: "rose" },
  ACW: { label: "후처리", tone: "violet" },
  Logout: { label: "로그아웃", tone: "gray" },
};

export const phaseInfo: Record<Phase, { label: string; tone: Tone; timer: string }> = {
  Idle: { label: "통화 없음", tone: "gray", timer: "통화 없이" },
  Ring: { label: "벨", tone: "amber", timer: "벨 울린 지" },
  Talk: { label: "통화", tone: "blue", timer: "방에 들어온 지" },
};

export const roomStateInfo: Record<RoomState, { label: string; tone: Tone }> = {
  Waiting: { label: "대기", tone: "amber" },
  Offering: { label: "상담원 부르는 중", tone: "amber" },
  Talking: { label: "통화중", tone: "blue" },
  Ended: { label: "종료", tone: "gray" },
};

export const warnLabel: Record<string, string> = {
  no_answer: "상담원 무응답",
  offer_timeout: "벨 시간 초과 (응답 없음)",
  call_gone: "재동기화: 엔진이 못 본 사이 끝난 콜",
  unknown_agent: "모르는 상담원",
  unknown_room: "모르는 방",
  invite_not_in_call: "통화 중이 아니라 부를 수 없음",
  invite_target_busy: "부를 상담원이 통화 중이거나 로그아웃",
  consult_in_progress: "이미 협의 중",
  consult_not_ready: "넘겨받을 상담원이 아직 방에 없음",
  no_consult: "협의 중이 아님",
  not_in_call: "통화 중이 아님",
  no_customer_channel: "고객 채널을 모름",
  bad_skills: "스킬 형식 오류",
  bad_record: "저장된 상담원 기록 오류",
};

/** 스킬 표시: "sales:1,support:2" → ["sales 1", "support 2"], 비면 모든 큐 */
export function skillChips(skills: string): string[] {
  if (!skills) return [];
  return skills.split(",").map((s) => s.replace(":", " "));
}

/** 방 이름에서 Linkedid 끝자리만: rq-support-1792992022.17 → …022.17 */
export function shortRoom(room: string): string {
  const id = room.replace(/^rq-[a-z]+-/, "");
  return id.length > 9 ? `…${id.slice(-6)}` : id;
}

export const SKILL_PRESETS = [
  { value: "", label: "모든 큐", hint: "스킬 없음" },
  { value: "support", label: "support" },
  { value: "sales", label: "sales" },
  { value: "support:1,sales:2", label: "support 우선, sales도", hint: "support:1,sales:2" },
  { value: "sales:1,support:2", label: "sales 우선, support도", hint: "sales:1,support:2" },
];

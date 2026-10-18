// 엔진(bin/engine)이 SSE로 보내는 줄의 타입과, 그 줄을 화면 상태로 접는 리듀서.
//
// 줄 형식: 상태머신 출력은 docs/06-state-model.md 10절, 접속 시 전체 상태(state)와 연결 상태(status)는
// engine/live/view.go. 화면은 엔진이 보낸 줄만으로 상태를 만든다 (엔진이 상태의 기준이다, D-008).

export type AgentState = "LoggedOut" | "NotReady" | "Ready" | "Ringing" | "Talking" | "Hold" | "ACW";
export type CallState = "Entered" | "Queued" | "Alerting" | "Connected" | "Held" | "Transferred" | "Ended";

export type AgentLine = {
  t: string;
  kind: "agent";
  id: string;
  name: string;
  from: AgentState;
  to: AgentState;
  reason: string;
  call: string;
};
export type CallLine = {
  t: string;
  kind: "call";
  id: string;
  from: CallState | "";
  to: CallState;
  queue: string;
  customer: string;
  agent: string;
};
export type CallEndLine = {
  t: string;
  kind: "call_end";
  id: string;
  outcome: string;
  queue: string;
  customer: string;
  agent: string;
  wait_ms: number;
  talk_ms: number;
};
export type ActionLine = { t: string; kind: "action"; action: string; iface: string; paused: boolean; reason: string };
export type WarnLine = { t: string; kind: "warn"; code: string; id: string };
export type StatusLine = { t: string; kind: "status"; connected: boolean; synced: boolean };
export type Line = AgentLine | CallLine | CallEndLine | ActionLine | WarnLine | StatusLine;

export type Totals = { handled: number; abandoned: number; timeout: number; other: number };

export type StateView = {
  kind: "state";
  now: string;
  queue: string;
  connected: boolean;
  synced: boolean;
  policy: { acw_ms: number; max_missed: number };
  snapshot: {
    agents: { id: string; name: string; state: AgentState; reason: string; call: string }[];
    calls: { id: string; state: CallState; queue: string; customer: string; agent: string }[];
  };
  since: { agents: Record<string, string>; calls: Record<string, string>; queued: Record<string, string> };
  totals: Totals;
  recent: Line[];
};

export type Agent = { id: string; name: string; state: AgentState; reason: string; call: string; since?: number };
export type Call = {
  id: string;
  state: CallState;
  queue: string;
  customer: string;
  agent: string;
  since?: number; // 지금 상태가 된 시각 (ms)
  queuedAt?: number; // 큐에 처음 들어온 시각 (ms) — 대기 시간
};
export type LogEntry = { seq: number; line: Line };

export type Link = "connecting" | "live" | "engine-down";

export type Board = {
  link: Link; // 화면 ↔ 엔진
  asterisk: boolean; // 엔진 ↔ Asterisk(AMI)
  synced: boolean;
  queue: string;
  acwMs: number;
  offsetMs: number; // 엔진 시계 − 브라우저 시계
  agents: Record<string, Agent>;
  calls: Record<string, Call>;
  customers: Record<string, string>; // 콜 ID → 고객 번호 (끝난 콜도 후처리 중에 보여 주려고 남긴다)
  ended: CallEndLine[]; // 최근 종료, 새것이 앞
  log: LogEntry[]; // 새것이 앞
  totals: Totals;
  seq: number;
};

const LOG_MAX = 200;
const ENDED_MAX = 12;

export const initialBoard: Board = {
  link: "connecting",
  asterisk: false,
  synced: false,
  queue: "support",
  acwMs: 10_000,
  offsetMs: 0,
  agents: {},
  calls: {},
  customers: {},
  ended: [],
  log: [],
  totals: { handled: 0, abandoned: 0, timeout: 0, other: 0 },
  seq: 0,
};

export type BoardAction =
  | { type: "state"; view: StateView; receivedAt: number }
  | { type: "line"; line: Line }
  | { type: "link"; link: Link };

/** "1792219447.519247"(초.마이크로초) → 밀리초 */
export const toMs = (t: string | undefined): number | undefined => (t ? Math.round(parseFloat(t) * 1000) : undefined);

export function reduce(b: Board, a: BoardAction): Board {
  switch (a.type) {
    case "link":
      // 엔진이 꺼진 동안 EventSource는 재접속을 거듭한다. 그사이 "연결 중"으로 깜빡이지 않게 둔다
      if (a.link === "connecting" && b.link === "engine-down") return b;
      return { ...b, link: a.link };
    case "state":
      return fromView(a.view, a.receivedAt);
    case "line":
      return applyLine(b, a.line);
  }
}

function fromView(v: StateView, receivedAt: number): Board {
  const agents: Record<string, Agent> = {};
  for (const a of v.snapshot.agents) agents[a.id] = { ...a, since: toMs(v.since.agents[a.id]) };
  const calls: Record<string, Call> = {};
  const customers: Record<string, string> = {};
  for (const c of v.snapshot.calls) {
    calls[c.id] = { ...c, since: toMs(v.since.calls[c.id]), queuedAt: toMs(v.since.queued?.[c.id]) };
    if (c.customer) customers[c.id] = c.customer;
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
    queue: v.queue,
    acwMs: v.policy?.acw_ms ?? 10_000,
    offsetMs: (toMs(v.now) ?? receivedAt) - receivedAt,
    agents,
    calls,
    customers,
    ended: ended.slice(0, ENDED_MAX),
    log: log.slice(0, LOG_MAX),
    totals: v.totals,
    seq,
  };
}

function applyLine(b: Board, line: Line): Board {
  const seq = b.seq + 1;
  const next: Board = { ...b, seq, log: [{ seq, line }, ...b.log].slice(0, LOG_MAX) };
  switch (line.kind) {
    case "status":
      next.asterisk = line.connected;
      next.synced = line.synced;
      break;
    case "agent":
      next.agents = {
        ...b.agents,
        [line.id]: { id: line.id, name: line.name, state: line.to, reason: line.reason, call: line.call, since: toMs(line.t) },
      };
      break;
    case "call": {
      const calls = { ...b.calls };
      if (line.to === "Ended") {
        delete calls[line.id];
      } else {
        const prev = calls[line.id];
        calls[line.id] = {
          id: line.id,
          state: line.to,
          queue: line.queue,
          customer: line.customer,
          agent: line.agent,
          since: toMs(line.t),
          queuedAt: prev?.queuedAt ?? (line.to === "Queued" ? toMs(line.t) : undefined),
        };
      }
      next.calls = calls;
      if (line.customer) next.customers = { ...b.customers, [line.id]: line.customer };
      break;
    }
    case "call_end":
      next.ended = [line, ...b.ended].slice(0, ENDED_MAX);
      next.totals = addOutcome(b.totals, line.outcome);
      if (line.customer) next.customers = { ...b.customers, [line.id]: line.customer };
      break;
    case "action":
    case "warn":
      break;
  }
  return next;
}

/** 엔진(view.go)과 같은 기준으로 결과를 센다. */
function addOutcome(t: Totals, outcome: string): Totals {
  if (outcome === "completed" || outcome.startsWith("completed:")) return { ...t, handled: t.handled + 1 };
  if (outcome === "abandoned") return { ...t, abandoned: t.abandoned + 1 };
  if (outcome === "timeout") return { ...t, timeout: t.timeout + 1 };
  return { ...t, other: t.other + 1 };
}

// ── 표시용 이름 ──────────────────────────────────────────────────────────

export type Tone = "green" | "amber" | "blue" | "orange" | "violet" | "rose" | "gray";

export const agentStateInfo: Record<AgentState, { label: string; tone: Tone; timer: string }> = {
  Ready: { label: "대기", tone: "green", timer: "대기한 지" },
  Ringing: { label: "벨울림", tone: "amber", timer: "벨 울린 지" },
  Talking: { label: "통화중", tone: "blue", timer: "통화한 지" },
  Hold: { label: "보류", tone: "orange", timer: "보류한 지" },
  ACW: { label: "후처리", tone: "violet", timer: "후처리 남은 시간" },
  NotReady: { label: "이석", tone: "rose", timer: "이석한 지" },
  LoggedOut: { label: "로그아웃", tone: "gray", timer: "로그아웃한 지" },
};

export const callStateInfo: Record<CallState, { label: string; tone: Tone }> = {
  Entered: { label: "인입", tone: "gray" },
  Queued: { label: "대기", tone: "amber" },
  Alerting: { label: "연결 중", tone: "amber" },
  Connected: { label: "통화중", tone: "blue" },
  Held: { label: "보류", tone: "orange" },
  Transferred: { label: "전환됨", tone: "violet" },
  Ended: { label: "종료", tone: "gray" },
};

export function outcomeInfo(outcome: string): { label: string; tone: Tone } {
  switch (outcome) {
    case "completed:agent":
      return { label: "응대 · 상담원 종료", tone: "green" };
    case "completed:caller":
      return { label: "응대 · 고객 종료", tone: "green" };
    case "completed:transfer":
      return { label: "응대 · 전환", tone: "violet" };
    case "completed":
      return { label: "응대", tone: "green" };
    case "abandoned":
      return { label: "포기", tone: "rose" };
    case "timeout":
      return { label: "큐 타임아웃", tone: "orange" };
    case "left_queue":
      return { label: "큐 이탈", tone: "gray" };
    case "no_queue":
      return { label: "큐 진입 전 종료", tone: "gray" };
    default:
      return { label: outcome, tone: "gray" };
  }
}

export const warnLabel: Record<string, string> = {
  resync_lost_channel: "재동기화: 끊긴 채널 정리",
  resync_new_channel: "재동기화: 새 채널 발견",
  resync_readd: "재동기화: 상담원을 큐에 다시 넣음",
  resync_adopt: "재동기화: 엔진이 모르던 상담원 채택",
  resync_pause: "재동기화: 이석 상태 다시 맞춤",
  unknown_agent: "모르는 상담원",
  called_during_acw: "후처리 중에 벨이 울림",
  called_while_not_ready: "이석 중에 벨이 울림",
  complete_missing: "AgentComplete 없이 통화가 끝남",
  complete_unexpected: "예상하지 못한 AgentComplete",
  ring_ended_without_event: "벨이 이벤트 없이 끝남",
  logout_in_call: "통화 중 로그아웃",
};

export const NOT_READY_REASONS = ["이석", "식사", "휴식", "교육", "회의"];

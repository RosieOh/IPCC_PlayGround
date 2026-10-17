//! 상태머신 코어. I/O가 없다: 같은 입력이면 항상 같은 출력이 나온다 (docs/06-state-model.md).
//!
//! 입력은 AMI 이벤트, 스냅샷(재동기화), 상담원 명령이다. 출력은 상태 변화·Asterisk에 보낼 액션·경고 줄이다.
//! 시간은 직접 읽지 않고 입력에 붙은 시각(마이크로초)만 쓴다. 타이머(후처리 끝, 콜 종료 확정)는
//! 다음 입력의 시각이 마감을 지나면 그 입력보다 먼저 처리한다.

use std::collections::{BTreeMap, BTreeSet};

use crate::ami::{fmt_ts, Message};
use crate::json::Out;

pub const REASON_ACW: &str = "후처리";
pub const REASON_NO_ANSWER: &str = "무응답";
pub const REASON_LOGIN: &str = "로그인";
pub const REASON_AWAY: &str = "이석";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AgentState {
    LoggedOut,
    NotReady,
    Ready,
    Ringing,
    Talking,
    Hold,
    Acw,
}

impl AgentState {
    pub fn as_str(self) -> &'static str {
        match self {
            AgentState::LoggedOut => "LoggedOut",
            AgentState::NotReady => "NotReady",
            AgentState::Ready => "Ready",
            AgentState::Ringing => "Ringing",
            AgentState::Talking => "Talking",
            AgentState::Hold => "Hold",
            AgentState::Acw => "ACW",
        }
    }

    fn in_call(self) -> bool {
        matches!(self, AgentState::Ringing | AgentState::Talking | AgentState::Hold)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CallState {
    Entered,
    Queued,
    Alerting,
    Connected,
    Held,
    Transferred,
    Ended,
}

impl CallState {
    pub fn as_str(self) -> &'static str {
        match self {
            CallState::Entered => "Entered",
            CallState::Queued => "Queued",
            CallState::Alerting => "Alerting",
            CallState::Connected => "Connected",
            CallState::Held => "Held",
            CallState::Transferred => "Transferred",
            CallState::Ended => "Ended",
        }
    }
}

#[derive(Clone, Debug)]
pub struct Agent {
    pub iface: String,
    pub name: String,
    pub state: AgentState,
    pub reason: String,
    /// 지금 맡은 콜. 후처리 중에는 방금 끝난 콜을 그대로 둔다.
    pub call: String,
    pub missed: u32,
    /// 통화 중에 누른 이석 (통화·후처리가 끝나면 이석으로 간다)
    pub reserved: String,
    pub acw_deadline: Option<i64>,
}

impl Agent {
    fn new(iface: &str, name: &str) -> Self {
        Agent {
            iface: iface.to_string(),
            name: name.to_string(),
            state: AgentState::LoggedOut,
            reason: String::new(),
            call: String::new(),
            missed: 0,
            reserved: String::new(),
            acw_deadline: None,
        }
    }
}

#[derive(Clone, Debug)]
pub struct Call {
    pub id: String,
    pub state: CallState,
    pub queue: String,
    pub customer: String,
    /// 지금 통화 중인 상담원
    pub agent: String,
    /// 이 콜을 받은 상담원 (전환 뒤에도 남는다)
    pub handled_by: String,
    pub queued_at: Option<i64>,
    pub connected_at: Option<i64>,
    pub complete_at: Option<i64>,
    pub ended_at: Option<i64>,
    /// 마지막 채널이 끊겼는데 AgentComplete가 아직 없을 때, 콜 종료를 확정할 시각
    pub finalize_deadline: Option<i64>,
    pub outcome: String,
    pub channels: BTreeSet<String>,
}

impl Call {
    fn new(id: &str) -> Self {
        Call {
            id: id.to_string(),
            state: CallState::Entered,
            queue: String::new(),
            customer: String::new(),
            agent: String::new(),
            handled_by: String::new(),
            queued_at: None,
            connected_at: None,
            complete_at: None,
            ended_at: None,
            finalize_deadline: None,
            outcome: String::new(),
            channels: BTreeSet::new(),
        }
    }
}

#[derive(Clone, Debug)]
struct Channel {
    linkedid: String,
    hangup_requested: bool,
}

/// 센터마다 다른 정책 값.
#[derive(Clone, Copy, Debug)]
pub struct Policy {
    /// 후처리 시간
    pub acw_us: i64,
    /// 무응답이 이만큼 연속되면 자동 이석 (0이면 끔)
    pub max_missed: u32,
    /// 마지막 채널이 끊긴 뒤 AgentComplete를 기다리는 시간
    pub finalize_grace_us: i64,
}

impl Default for Policy {
    fn default() -> Self {
        Policy { acw_us: 10_000_000, max_missed: 2, finalize_grace_us: 2_000_000 }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CommandKind {
    Login,
    Logout,
    Ready,
    NotReady,
    WrapUp,
}

/// 상담원이 화면에서 누른 버튼.
#[derive(Clone, Debug)]
pub struct Command {
    pub kind: CommandKind,
    pub iface: String,
    /// Login이면 이름, NotReady면 사유
    pub arg: String,
}

#[derive(Clone, Debug)]
pub enum Input {
    Event(Message),
    /// CoreShowChannels 결과 (CoreShowChannel 이벤트들)
    ChannelSnapshot(Vec<Message>),
    /// QueueStatus 결과 (QueueParams/QueueMember/QueueEntry 이벤트들)
    QueueSnapshot(Vec<Message>),
    Command(Command),
}

pub struct Engine {
    pub policy: Policy,
    agents: BTreeMap<String, Agent>,
    calls: BTreeMap<String, Call>,
    channels: BTreeMap<String, Channel>,
    now: i64,
}

impl Engine {
    pub fn new(policy: Policy) -> Self {
        Engine { policy, agents: BTreeMap::new(), calls: BTreeMap::new(), channels: BTreeMap::new(), now: 0 }
    }

    /// 입력 하나를 처리한다. 시각이 거꾸로 가면 지금까지의 가장 늦은 시각으로 본다.
    pub fn apply(&mut self, ts: i64, input: &Input) -> Vec<Out> {
        let mut out = Vec::new();
        let t = ts.max(self.now);
        self.fire_timers(t, &mut out);
        self.now = t;
        match input {
            Input::Event(m) => self.on_event(m, t, &mut out),
            Input::ChannelSnapshot(items) => self.on_channel_snapshot(items, t, &mut out),
            Input::QueueSnapshot(items) => self.on_queue_snapshot(items, t, &mut out),
            Input::Command(c) => self.on_command(c, t, &mut out),
        }
        out
    }

    /// 입력 없이 시간만 ts까지 흘려서 마감이 지난 타이머를 처리한다 (실시간으로 돌릴 때 주기적으로 부른다).
    pub fn advance(&mut self, ts: i64) -> Vec<Out> {
        let mut out = Vec::new();
        let t = ts.max(self.now);
        self.fire_timers(t, &mut out);
        self.now = t;
        out
    }

    /// 남은 타이머를 모두 처리한다 (녹화 재생의 끝에서 시간을 끝까지 흘린다).
    pub fn flush(&mut self) -> Vec<Out> {
        let mut out = Vec::new();
        self.fire_timers(i64::MAX, &mut out);
        out
    }

    pub fn snapshot(&self) -> Out {
        let agents: Vec<String> = self
            .agents
            .values()
            .map(|a| {
                Out::new()
                    .s("id", &a.iface)
                    .s("name", &a.name)
                    .s("state", a.state.as_str())
                    .s("reason", &a.reason)
                    .s("call", &a.call)
                    .line()
            })
            .collect();
        let calls: Vec<String> = self
            .calls
            .values()
            .map(|c| {
                Out::new()
                    .s("id", &c.id)
                    .s("state", c.state.as_str())
                    .s("queue", &c.queue)
                    .s("customer", &c.customer)
                    .s("agent", &c.agent)
                    .line()
            })
            .collect();
        Out::new()
            .s("kind", "snapshot")
            .raw("agents", format!("[{}]", agents.join(",")))
            .raw("calls", format!("[{}]", calls.join(",")))
    }

    pub fn agent(&self, iface: &str) -> Option<&Agent> {
        self.agents.get(iface)
    }

    // ── 타이머 ────────────────────────────────────────────────────────────

    /// 마감이 until 이하인 타이머를 (마감, 종류, ID) 순서로 하나씩 처리한다.
    /// 처리하면서 새 타이머가 생길 수 있어서 매번 다시 찾는다.
    fn fire_timers(&mut self, until: i64, out: &mut Vec<Out>) {
        loop {
            let mut next: Option<(i64, u8, String)> = None;
            for a in self.agents.values() {
                if let Some(d) = a.acw_deadline {
                    let c = (d, 0u8, a.iface.clone());
                    if d <= until && next.as_ref().map_or(true, |n| c < *n) {
                        next = Some(c);
                    }
                }
            }
            for call in self.calls.values() {
                if let Some(d) = call.finalize_deadline {
                    let c = (d, 1u8, call.id.clone());
                    if d <= until && next.as_ref().map_or(true, |n| c < *n) {
                        next = Some(c);
                    }
                }
            }
            let Some((d, kind, id)) = next else { break };
            if d > self.now {
                self.now = d;
            }
            let t = self.now;
            if kind == 0 {
                self.end_acw(&id, t, out);
            } else {
                self.finalize_expired(&id, t, out);
            }
        }
    }

    // ── 이벤트 ────────────────────────────────────────────────────────────

    fn on_event(&mut self, m: &Message, t: i64, out: &mut Vec<Out>) {
        match m.event() {
            "Newchannel" => self.add_channel(m.get("Uniqueid"), m.get("Linkedid"), m.get("CallerIDNum"), t, out),
            "NewCallerid" => self.update_customer(m.get("Uniqueid"), m.get("Linkedid"), m.get("CallerIDNum")),
            "HangupRequest" | "SoftHangupRequest" => {
                if let Some(ch) = self.channels.get_mut(m.get("Uniqueid")) {
                    ch.hangup_requested = true;
                }
            }
            "Hangup" => self.remove_channel(m.get("Uniqueid"), t, out),
            "QueueCallerJoin" => self.on_queue_join(m, t, out),
            "QueueCallerAbandon" => self.on_abandon(m, t, out),
            "AgentCalled" => self.on_agent_called(m, t, out),
            "AgentRingNoAnswer" => self.on_ring_no_answer(m, t, out),
            "AgentConnect" => self.on_agent_connect(m, t, out),
            "AgentComplete" => self.on_agent_complete(m, t, out),
            "Hold" => self.on_hold(m.get("Linkedid"), true, t, out),
            "Unhold" => self.on_hold(m.get("Linkedid"), false, t, out),
            "AttendedTransfer" => self.on_attended_transfer(m, t, out),
            "QueueMemberAdded" => self.member_seen(
                m.get("Interface"),
                m.get("MemberName"),
                m.get("Paused") == "1",
                m.get("PausedReason"),
                t,
                out,
            ),
            "QueueMemberRemoved" => self.logout(m.get("Interface"), t, out),
            "QueueMemberPause" => {
                self.on_member_pause(m.get("Interface"), m.get("Paused") == "1", m.get("PausedReason"), t, out)
            }
            _ => {}
        }
    }

    fn add_channel(&mut self, uid: &str, lid: &str, num: &str, t: i64, out: &mut Vec<Out>) {
        if uid.is_empty() || lid.is_empty() {
            return;
        }
        self.channels.insert(uid.to_string(), Channel { linkedid: lid.to_string(), hangup_requested: false });
        let is_new = !self.calls.contains_key(lid);
        let c = self.calls.entry(lid.to_string()).or_insert_with(|| Call::new(lid));
        c.channels.insert(uid.to_string());
        if uid == lid && c.customer.is_empty() && valid_num(num) {
            c.customer = num.to_string();
        }
        if is_new {
            out.push(call_line(t, c, ""));
        }
    }

    /// 콜을 만든 채널(Uniqueid = Linkedid)의 번호가 정해지면 고객 번호로 쓴다.
    fn update_customer(&mut self, uid: &str, lid: &str, num: &str) {
        if uid.is_empty() || uid != lid || !valid_num(num) {
            return;
        }
        if let Some(c) = self.calls.get_mut(lid) {
            if c.customer.is_empty() {
                c.customer = num.to_string();
            }
        }
    }

    fn remove_channel(&mut self, uid: &str, t: i64, out: &mut Vec<Out>) {
        let Some(ch) = self.channels.remove(uid) else { return };
        let lid = ch.linkedid;
        let empty = match self.calls.get_mut(&lid) {
            Some(c) => {
                c.channels.remove(uid);
                c.channels.is_empty()
            }
            None => false,
        };
        if empty {
            self.last_channel_gone(&lid, t, out);
        }
    }

    /// 콜의 마지막 채널이 끊겼다. AgentComplete가 아직 안 왔으면 잠깐 기다린다 (Hangup보다 늦게 올 수 있다).
    fn last_channel_gone(&mut self, lid: &str, t: i64, out: &mut Vec<Out>) {
        let Some(c) = self.calls.get_mut(lid) else { return };
        c.ended_at = Some(t);
        let agent_busy = !c.agent.is_empty()
            && c.complete_at.is_none()
            && self
                .agents
                .get(&c.agent)
                .map_or(false, |a| matches!(a.state, AgentState::Talking | AgentState::Hold) && a.call == lid);
        if agent_busy {
            c.finalize_deadline = Some(t + self.policy.finalize_grace_us);
            return;
        }
        // 이 콜로 벨이 울리던 상담원이 있으면 되돌린다 (콜이 사라졌다)
        let ringing: Vec<String> = self
            .agents
            .values()
            .filter(|a| a.state == AgentState::Ringing && a.call == lid)
            .map(|a| a.iface.clone())
            .collect();
        for iface in ringing {
            out.push(warn(t, "ring_ended_without_event", &iface));
            self.back_from_ring(&iface, t, out);
        }
        self.finalize_call(lid, t, out);
    }

    fn finalize_expired(&mut self, lid: &str, t: i64, out: &mut Vec<Out>) {
        let agent = match self.calls.get(lid) {
            Some(c) => c.agent.clone(),
            None => return,
        };
        let busy = self
            .agents
            .get(&agent)
            .map_or(false, |a| matches!(a.state, AgentState::Talking | AgentState::Hold) && a.call == lid);
        if busy {
            out.push(warn(t, "complete_missing", &agent));
            self.enter_acw(&agent, t, out);
        }
        self.finalize_call(lid, t, out);
    }

    fn finalize_call(&mut self, lid: &str, t: i64, out: &mut Vec<Out>) {
        let Some(mut c) = self.calls.remove(lid) else { return };
        let from = c.state;
        c.state = CallState::Ended;
        out.push(call_line(t, &c, from.as_str()));
        let outcome = if !c.outcome.is_empty() {
            c.outcome.clone()
        } else if c.connected_at.is_some() {
            "completed".to_string()
        } else if c.queued_at.is_some() {
            "left_queue".to_string()
        } else {
            "no_queue".to_string()
        };
        let end = c.ended_at.unwrap_or(t);
        let wait_ms = c.queued_at.map_or(0, |q| (c.connected_at.unwrap_or(end) - q) / 1000);
        let talk_ms = c.connected_at.map_or(0, |s| (c.complete_at.unwrap_or(end) - s) / 1000);
        out.push(
            Out::new()
                .s("t", &fmt_ts(t))
                .s("kind", "call_end")
                .s("id", &c.id)
                .s("outcome", &outcome)
                .s("queue", &c.queue)
                .s("customer", &c.customer)
                .s("agent", &c.handled_by)
                .i("wait_ms", wait_ms)
                .i("talk_ms", talk_ms),
        );
    }

    fn on_queue_join(&mut self, m: &Message, t: i64, out: &mut Vec<Out>) {
        let lid = m.get("Linkedid");
        let Some(c) = self.calls.get_mut(lid) else { return };
        c.queue = m.get("Queue").to_string();
        let num = m.get("CallerIDNum");
        if valid_num(num) {
            c.customer = num.to_string();
        }
        c.queued_at = Some(t);
        self.set_call(lid, CallState::Queued, t, out);
    }

    /// QueueCallerAbandon은 큐 타임아웃 때도 온다 (app_queue가 record_abandoned를 부른다).
    /// 고객 채널에 끊기 요청이 먼저 있었으면 포기, 없었으면 타임아웃이다.
    ///
    /// 벨이 울리던 상담원은 대기로 돌린다. 벨 중에 고객이 끊으면 상담원 쪽 이벤트(AgentRingNoAnswer 등)는
    /// 오지 않는다 (testdata/ring-abandon.ami). 상담원 잘못이 아니므로 무응답 횟수는 세지 않는다.
    fn on_abandon(&mut self, m: &Message, t: i64, out: &mut Vec<Out>) {
        let lid = m.get("Linkedid");
        let caller_hung_up = self.channels.get(m.get("Uniqueid")).map_or(true, |ch| ch.hangup_requested);
        if let Some(c) = self.calls.get_mut(lid) {
            c.outcome = if caller_hung_up { "abandoned" } else { "timeout" }.to_string();
        }
        let ringing: Vec<String> = self
            .agents
            .values()
            .filter(|a| a.state == AgentState::Ringing && a.call == lid)
            .map(|a| a.iface.clone())
            .collect();
        for iface in ringing {
            self.back_from_ring(&iface, t, out);
        }
    }

    fn on_agent_called(&mut self, m: &Message, t: i64, out: &mut Vec<Out>) {
        let iface = m.get("Interface");
        if iface.is_empty() {
            return;
        }
        self.ensure_agent(iface, m.get("MemberName"), t, out);
        let lid = m.get("Linkedid").to_string();
        let state = self.agents[iface].state;
        match state {
            AgentState::Acw => {
                out.push(warn(t, "called_during_acw", iface));
                self.agents.get_mut(iface).unwrap().acw_deadline = None;
            }
            AgentState::NotReady => out.push(warn(t, "called_while_not_ready", iface)),
            _ => {}
        }
        self.set_agent(iface, AgentState::Ringing, "", &lid, t, out);
        if self.calls.get(&lid).map_or(false, |c| c.state == CallState::Queued) {
            self.set_call(&lid, CallState::Alerting, t, out);
        }
    }

    fn on_ring_no_answer(&mut self, m: &Message, t: i64, out: &mut Vec<Out>) {
        let iface = m.get("Interface");
        let Some(a) = self.agents.get_mut(iface) else {
            out.push(warn(t, "unknown_agent", iface));
            return;
        };
        if a.state != AgentState::Ringing {
            return;
        }
        let lid = a.call.clone();
        a.missed += 1;
        if self.policy.max_missed > 0 && a.missed >= self.policy.max_missed {
            a.missed = 0;
            self.go_not_ready(iface, REASON_NO_ANSWER, t, out);
        } else {
            self.back_from_ring(iface, t, out);
        }
        if self.calls.get(&lid).map_or(false, |c| c.state == CallState::Alerting) {
            self.set_call(&lid, CallState::Queued, t, out);
        }
    }

    fn on_agent_connect(&mut self, m: &Message, t: i64, out: &mut Vec<Out>) {
        let iface = m.get("Interface");
        if iface.is_empty() {
            return;
        }
        self.ensure_agent(iface, m.get("MemberName"), t, out);
        let mut lid = m.get("Linkedid").to_string();
        if lid.is_empty() {
            lid = self.agents[iface].call.clone();
        }
        {
            let a = self.agents.get_mut(iface).unwrap();
            a.missed = 0;
            a.acw_deadline = None;
        }
        self.set_agent(iface, AgentState::Talking, "", &lid, t, out);
        if let Some(c) = self.calls.get_mut(&lid) {
            c.agent = iface.to_string();
            c.handled_by = iface.to_string();
            c.connected_at = Some(t);
        }
        self.set_call(&lid, CallState::Connected, t, out);
    }

    /// AgentComplete는 채널 정보 없이 올 수 있다. 상담원(Interface)으로 찾고, 콜은 그 상담원의 call로 찾는다.
    fn on_agent_complete(&mut self, m: &Message, t: i64, out: &mut Vec<Out>) {
        let iface = m.get("Interface");
        let Some(a) = self.agents.get(iface) else {
            out.push(warn(t, "unknown_agent", iface));
            return;
        };
        let lid = a.call.clone();
        if matches!(a.state, AgentState::Talking | AgentState::Hold) {
            self.enter_acw(iface, t, out);
        } else {
            out.push(warn(t, "complete_unexpected", iface));
        }
        let reason = m.get("Reason");
        let mut finalize = false;
        let mut transferred = false;
        if let Some(c) = self.calls.get_mut(&lid) {
            c.complete_at = Some(t);
            c.outcome = if reason.is_empty() { "completed".to_string() } else { format!("completed:{reason}") };
            finalize = c.finalize_deadline.is_some();
            if reason == "transfer" && !finalize {
                c.agent.clear();
                transferred = true;
            }
        }
        if transferred {
            self.set_call(&lid, CallState::Transferred, t, out);
        }
        if finalize {
            self.finalize_call(&lid, t, out);
        }
    }

    /// Hold/Unhold는 보류를 건 쪽(상담원 채널)에 온다. 콜의 상담원이 통화 중일 때만 반영한다.
    /// 블라인드 전환 때는 Hold 없이 Unhold만 오기도 해서, 보류 중이 아니면 무시한다.
    fn on_hold(&mut self, lid: &str, hold: bool, t: i64, out: &mut Vec<Out>) {
        let agent = match self.calls.get(lid) {
            Some(c) => c.agent.clone(),
            None => return,
        };
        if agent.is_empty() {
            return;
        }
        match (hold, self.agents.get(&agent).map(|a| a.state)) {
            (true, Some(AgentState::Talking)) => {
                self.set_agent(&agent, AgentState::Hold, "", lid, t, out);
                self.set_call(lid, CallState::Held, t, out);
            }
            (false, Some(AgentState::Hold)) => {
                self.set_agent(&agent, AgentState::Talking, "", lid, t, out);
                self.set_call(lid, CallState::Connected, t, out);
            }
            _ => {}
        }
    }

    /// 어텐디드 전환 중에 *3(atxferthreeway)을 누르면 3자통화가 되고 AttendedTransfer DestType=Threeway가 온다.
    /// 전환을 시작할 때 온 Hold의 짝인 Unhold는 오지 않으므로 이 이벤트로 보류를 푼다 (testdata/three-way.ami).
    /// 일반 어텐디드 전환(DestType=Bridge)은 상담원이 빠지면서 AgentComplete Reason=transfer로 끝난다.
    fn on_attended_transfer(&mut self, m: &Message, t: i64, out: &mut Vec<Out>) {
        if m.get("Result") == "Success" && m.get("DestType") == "Threeway" {
            self.on_hold(m.get("OrigTransfererLinkedid"), false, t, out);
        }
    }

    /// QueueMemberAdded 또는 스냅샷에서 상담원을 본다. 엔진이 모르는 로그인이면 Asterisk 쪽 상태를 따른다.
    fn member_seen(&mut self, iface: &str, name: &str, paused: bool, reason: &str, t: i64, out: &mut Vec<Out>) {
        if iface.is_empty() {
            return;
        }
        let a = self.agents.entry(iface.to_string()).or_insert_with(|| Agent::new(iface, name));
        if !name.is_empty() {
            a.name = name.to_string();
        }
        if a.state != AgentState::LoggedOut {
            return;
        }
        if paused {
            let r = if reason.is_empty() { REASON_LOGIN } else { reason };
            self.set_agent(iface, AgentState::NotReady, r, "", t, out);
        } else {
            self.set_agent(iface, AgentState::Ready, "", "", t, out);
        }
    }

    fn logout(&mut self, iface: &str, t: i64, out: &mut Vec<Out>) {
        let Some(a) = self.agents.get_mut(iface) else { return };
        if a.state == AgentState::LoggedOut {
            return;
        }
        if a.state.in_call() {
            out.push(warn(t, "logout_in_call", iface));
        }
        a.acw_deadline = None;
        a.reserved.clear();
        a.missed = 0;
        self.set_agent(iface, AgentState::LoggedOut, "", "", t, out);
    }

    /// Asterisk 쪽의 이석/대기 변화. 엔진이 보낸 QueuePause의 메아리면 상태가 이미 같아서 아무 일도 없다.
    fn on_member_pause(&mut self, iface: &str, paused: bool, reason: &str, t: i64, out: &mut Vec<Out>) {
        let Some(a) = self.agents.get_mut(iface) else { return };
        let state = a.state;
        if paused {
            let r = if reason.is_empty() { REASON_AWAY } else { reason };
            match state {
                AgentState::Ready | AgentState::NotReady => self.set_agent(iface, AgentState::NotReady, r, "", t, out),
                AgentState::Ringing | AgentState::Talking | AgentState::Hold => a.reserved = r.to_string(),
                AgentState::Acw => {
                    if r != REASON_ACW {
                        a.reserved = r.to_string();
                    }
                }
                AgentState::LoggedOut => {}
            }
        } else {
            match state {
                AgentState::NotReady => self.set_agent(iface, AgentState::Ready, "", "", t, out),
                AgentState::Acw => {
                    a.acw_deadline = None;
                    a.reserved.clear();
                    self.set_agent(iface, AgentState::Ready, "", "", t, out);
                }
                AgentState::Ringing | AgentState::Talking | AgentState::Hold => a.reserved.clear(),
                AgentState::Ready | AgentState::LoggedOut => {}
            }
        }
    }

    // ── 스냅샷 (재동기화) ─────────────────────────────────────────────────

    /// CoreShowChannels 결과와 메모리의 채널을 맞춘다.
    fn on_channel_snapshot(&mut self, items: &[Message], t: i64, out: &mut Vec<Out>) {
        let seen: BTreeSet<String> =
            items.iter().map(|m| m.get("Uniqueid").to_string()).filter(|u| !u.is_empty()).collect();
        let lost: Vec<String> = self.channels.keys().filter(|u| !seen.contains(*u)).cloned().collect();
        for uid in lost {
            out.push(warn(t, "resync_lost_channel", &uid));
            self.remove_channel(&uid, t, out);
        }
        for m in items {
            let uid = m.get("Uniqueid");
            if uid.is_empty() || self.channels.contains_key(uid) {
                continue;
            }
            out.push(warn(t, "resync_new_channel", uid));
            self.add_channel(uid, m.get("Linkedid"), m.get("CallerIDNum"), t, out);
        }
    }

    /// QueueStatus 결과와 상담원을 맞춘다. 상담원 상태의 기준은 엔진이라, 어긋나면 Asterisk 쪽을 고친다.
    fn on_queue_snapshot(&mut self, items: &[Message], t: i64, out: &mut Vec<Out>) {
        let mut members: BTreeMap<String, &Message> = BTreeMap::new();
        for m in items {
            // 스냅샷의 QueueMember는 실시간 이벤트와 필드 이름이 다르다 (Name/Location)
            if m.event() == "QueueMember" && !m.get("Location").is_empty() {
                members.insert(m.get("Location").to_string(), m);
            }
        }
        let missing: Vec<(String, bool, String)> = self
            .agents
            .values()
            .filter(|a| a.state != AgentState::LoggedOut && !members.contains_key(&a.iface))
            .map(|a| (a.iface.clone(), matches!(a.state, AgentState::NotReady | AgentState::Acw), a.reason.clone()))
            .collect();
        for (iface, paused, reason) in missing {
            out.push(warn(t, "resync_readd", &iface));
            out.push(action(t, "QueueAdd", &iface, paused, &reason));
        }
        for (iface, m) in &members {
            let known = self.agents.get(iface).map_or(false, |a| a.state != AgentState::LoggedOut);
            if !known {
                out.push(warn(t, "resync_adopt", iface));
                self.member_seen(iface, m.get("Name"), m.get("Paused") == "1", m.get("PausedReason"), t, out);
                continue;
            }
            let a = &self.agents[iface];
            if a.state.in_call() {
                continue;
            }
            let want = matches!(a.state, AgentState::NotReady | AgentState::Acw);
            if want != (m.get("Paused") == "1") {
                let reason = if want { a.reason.clone() } else { String::new() };
                out.push(warn(t, "resync_pause", iface));
                out.push(action(t, "QueuePause", iface, want, &reason));
            }
        }
    }

    // ── 명령 ──────────────────────────────────────────────────────────────

    fn on_command(&mut self, c: &Command, t: i64, out: &mut Vec<Out>) {
        let iface = c.iface.as_str();
        let state = self.agents.get(iface).map(|a| a.state);
        match c.kind {
            CommandKind::Login => {
                let a = self.agents.entry(iface.to_string()).or_insert_with(|| Agent::new(iface, &c.arg));
                if !c.arg.is_empty() {
                    a.name = c.arg.clone();
                }
                if a.state != AgentState::LoggedOut {
                    return;
                }
                self.set_agent(iface, AgentState::NotReady, REASON_LOGIN, "", t, out);
                out.push(action(t, "QueueAdd", iface, true, REASON_LOGIN));
            }
            CommandKind::Logout => {
                if matches!(state, None | Some(AgentState::LoggedOut)) {
                    return;
                }
                self.logout(iface, t, out);
                out.push(action(t, "QueueRemove", iface, false, ""));
            }
            CommandKind::Ready => match state {
                Some(AgentState::NotReady) | Some(AgentState::Acw) => {
                    let a = self.agents.get_mut(iface).unwrap();
                    a.acw_deadline = None;
                    a.reserved.clear();
                    self.set_agent(iface, AgentState::Ready, "", "", t, out);
                    out.push(action(t, "QueuePause", iface, false, ""));
                }
                Some(AgentState::Ringing) | Some(AgentState::Talking) | Some(AgentState::Hold) => {
                    self.agents.get_mut(iface).unwrap().reserved.clear();
                }
                _ => {}
            },
            CommandKind::NotReady => {
                let reason = if c.arg.is_empty() { REASON_AWAY } else { c.arg.as_str() };
                match state {
                    Some(AgentState::Ready) | Some(AgentState::NotReady) | Some(AgentState::Acw) => {
                        self.go_not_ready(iface, reason, t, out);
                    }
                    Some(AgentState::Ringing) | Some(AgentState::Talking) | Some(AgentState::Hold) => {
                        // 통화를 끊지 않는다: 이석을 예약해 두었다가 통화·후처리가 끝나면 이석으로 간다
                        self.agents.get_mut(iface).unwrap().reserved = reason.to_string();
                    }
                    _ => {}
                }
            }
            CommandKind::WrapUp => self.end_acw(iface, t, out),
        }
    }

    // ── 상태 바꾸기 ───────────────────────────────────────────────────────

    /// 엔진이 모르는 상담원이 큐 이벤트에 나오면 대기 상태로 만든다 (녹화가 로그인 뒤에 시작된 경우 등).
    fn ensure_agent(&mut self, iface: &str, name: &str, t: i64, out: &mut Vec<Out>) {
        let a = self.agents.entry(iface.to_string()).or_insert_with(|| Agent::new(iface, name));
        if a.name.is_empty() {
            a.name = name.to_string();
        }
        if a.state == AgentState::LoggedOut {
            a.state = AgentState::Ready;
            out.push(warn(t, "unknown_agent", iface));
        }
    }

    fn set_agent(&mut self, iface: &str, state: AgentState, reason: &str, call: &str, t: i64, out: &mut Vec<Out>) {
        let Some(a) = self.agents.get_mut(iface) else { return };
        let from = a.state;
        let changed = from != state || a.reason != reason;
        a.state = state;
        a.reason = reason.to_string();
        a.call = call.to_string();
        if changed {
            out.push(
                Out::new()
                    .s("t", &fmt_ts(t))
                    .s("kind", "agent")
                    .s("id", &a.iface)
                    .s("name", &a.name)
                    .s("from", from.as_str())
                    .s("to", a.state.as_str())
                    .s("reason", &a.reason)
                    .s("call", &a.call),
            );
        }
    }

    fn set_call(&mut self, id: &str, state: CallState, t: i64, out: &mut Vec<Out>) {
        let Some(c) = self.calls.get_mut(id) else { return };
        if c.state == state {
            return;
        }
        let from = c.state;
        c.state = state;
        out.push(call_line(t, c, from.as_str()));
    }

    fn enter_acw(&mut self, iface: &str, t: i64, out: &mut Vec<Out>) {
        let call = self.agents[iface].call.clone();
        self.set_agent(iface, AgentState::Acw, REASON_ACW, &call, t, out);
        self.agents.get_mut(iface).unwrap().acw_deadline = Some(t + self.policy.acw_us);
        out.push(action(t, "QueuePause", iface, true, REASON_ACW));
    }

    /// 후처리가 끝났다 (타이머 또는 상담원 버튼). 예약된 이석이 있으면 이석으로, 없으면 대기로.
    fn end_acw(&mut self, iface: &str, t: i64, out: &mut Vec<Out>) {
        let Some(a) = self.agents.get_mut(iface) else { return };
        a.acw_deadline = None;
        if a.state != AgentState::Acw {
            return;
        }
        let reserved = std::mem::take(&mut a.reserved);
        if reserved.is_empty() {
            self.set_agent(iface, AgentState::Ready, "", "", t, out);
            out.push(action(t, "QueuePause", iface, false, ""));
        } else {
            self.go_not_ready(iface, &reserved, t, out);
        }
    }

    /// 벨이 울리다 끝났다 (무응답, 콜 사라짐). 예약된 이석이 있으면 이석으로.
    fn back_from_ring(&mut self, iface: &str, t: i64, out: &mut Vec<Out>) {
        let reserved = match self.agents.get_mut(iface) {
            Some(a) => std::mem::take(&mut a.reserved),
            None => return,
        };
        if reserved.is_empty() {
            self.set_agent(iface, AgentState::Ready, "", "", t, out);
        } else {
            self.go_not_ready(iface, &reserved, t, out);
        }
    }

    fn go_not_ready(&mut self, iface: &str, reason: &str, t: i64, out: &mut Vec<Out>) {
        let Some(a) = self.agents.get_mut(iface) else { return };
        if a.state == AgentState::NotReady && a.reason == reason {
            return;
        }
        a.acw_deadline = None;
        a.reserved.clear();
        self.set_agent(iface, AgentState::NotReady, reason, "", t, out);
        out.push(action(t, "QueuePause", iface, true, reason));
    }
}

fn valid_num(n: &str) -> bool {
    !n.is_empty() && n != "<unknown>"
}

fn call_line(t: i64, c: &Call, from: &str) -> Out {
    Out::new()
        .s("t", &fmt_ts(t))
        .s("kind", "call")
        .s("id", &c.id)
        .s("from", from)
        .s("to", c.state.as_str())
        .s("queue", &c.queue)
        .s("customer", &c.customer)
        .s("agent", &c.agent)
}

fn action(t: i64, name: &str, iface: &str, paused: bool, reason: &str) -> Out {
    Out::new()
        .s("t", &fmt_ts(t))
        .s("kind", "action")
        .s("action", name)
        .s("iface", iface)
        .b("paused", paused)
        .s("reason", reason)
}

fn warn(t: i64, code: &str, id: &str) -> Out {
    Out::new().s("t", &fmt_ts(t)).s("kind", "warn").s("code", code).s("id", id)
}

#[cfg(test)]
mod tests {
    use super::*;

    const AGENT: &str = "Local/1001@sim-agent";

    fn ev(name: &str, kv: &[(&str, &str)]) -> Input {
        let mut fields = vec![("Event".to_string(), name.to_string())];
        fields.extend(kv.iter().map(|(k, v)| (k.to_string(), v.to_string())));
        Input::Event(Message { fields })
    }

    fn cmd(kind: CommandKind, arg: &str) -> Input {
        Input::Command(Command { kind, iface: AGENT.into(), arg: arg.into() })
    }

    /// 로그인 → 고객 채널 2개(Linkedid L) → 큐 → 벨 → 연결까지 만든다.
    fn connected(e: &mut Engine) -> Vec<Out> {
        let mut out = Vec::new();
        let steps = [
            ev("QueueMemberAdded", &[("Interface", AGENT), ("MemberName", "SIM-1001"), ("Paused", "0")]),
            ev("Newchannel", &[("Uniqueid", "L"), ("Linkedid", "L"), ("CallerIDNum", "01012345678")]),
            ev("Newchannel", &[("Uniqueid", "A"), ("Linkedid", "L"), ("CallerIDNum", "<unknown>")]),
            ev("QueueCallerJoin", &[("Queue", "support"), ("Uniqueid", "L"), ("Linkedid", "L"), ("CallerIDNum", "01012345678")]),
            ev("AgentCalled", &[("Interface", AGENT), ("MemberName", "SIM-1001"), ("Linkedid", "L")]),
            ev("AgentConnect", &[("Interface", AGENT), ("MemberName", "SIM-1001"), ("Linkedid", "L")]),
        ];
        for (i, s) in steps.iter().enumerate() {
            out.extend(e.apply(1_000_000 * (i as i64 + 1), s));
        }
        out
    }

    fn has(out: &[Out], kind: &str, key: &str, val: &str) -> bool {
        out.iter().any(|o| o.get("kind") == kind && o.get(key) == val)
    }

    #[test]
    fn complete_after_all_hangups_still_gets_its_reason() {
        let mut e = Engine::new(Policy::default());
        connected(&mut e);
        e.apply(10_000_000, &ev("Hangup", &[("Uniqueid", "A")]));
        let out = e.apply(10_000_100, &ev("Hangup", &[("Uniqueid", "L")]));
        assert!(!has(&out, "call_end", "id", "L"), "AgentComplete를 기다려야 한다");
        // 채널 정보 없는 AgentComplete (실측)
        let out = e.apply(10_000_200, &ev("AgentComplete", &[("Interface", AGENT), ("Reason", "agent")]));
        assert!(has(&out, "call_end", "outcome", "completed:agent"));
        assert_eq!(e.agent(AGENT).unwrap().state, AgentState::Acw);
    }

    #[test]
    fn missing_complete_is_finalized_by_timer() {
        let mut e = Engine::new(Policy::default());
        connected(&mut e);
        e.apply(10_000_000, &ev("Hangup", &[("Uniqueid", "A")]));
        e.apply(10_000_000, &ev("Hangup", &[("Uniqueid", "L")]));
        let out = e.flush();
        assert!(has(&out, "warn", "code", "complete_missing"));
        assert!(has(&out, "call_end", "outcome", "completed"));
    }

    #[test]
    fn abandon_versus_timeout() {
        for (hangup_first, want) in [(true, "abandoned"), (false, "timeout")] {
            let mut e = Engine::new(Policy::default());
            e.apply(1, &ev("Newchannel", &[("Uniqueid", "L"), ("Linkedid", "L")]));
            e.apply(2, &ev("QueueCallerJoin", &[("Queue", "support"), ("Uniqueid", "L"), ("Linkedid", "L")]));
            if hangup_first {
                e.apply(3, &ev("HangupRequest", &[("Uniqueid", "L")]));
            }
            e.apply(4, &ev("QueueCallerAbandon", &[("Uniqueid", "L"), ("Linkedid", "L")]));
            let out = e.apply(5, &ev("Hangup", &[("Uniqueid", "L")]));
            assert!(has(&out, "call_end", "outcome", want), "{want}");
        }
    }

    #[test]
    fn caller_leaving_while_ringing_returns_agent_without_warning() {
        let mut e = Engine::new(Policy::default());
        e.apply(1, &ev("QueueMemberAdded", &[("Interface", AGENT), ("Paused", "0")]));
        e.apply(2, &ev("Newchannel", &[("Uniqueid", "L"), ("Linkedid", "L")]));
        e.apply(3, &ev("QueueCallerJoin", &[("Queue", "support"), ("Uniqueid", "L"), ("Linkedid", "L")]));
        e.apply(4, &ev("AgentCalled", &[("Interface", AGENT), ("Linkedid", "L")]));
        e.apply(5, &ev("HangupRequest", &[("Uniqueid", "L")]));
        let mut out = e.apply(6, &ev("QueueCallerAbandon", &[("Uniqueid", "L"), ("Linkedid", "L")]));
        out.extend(e.apply(7, &ev("Hangup", &[("Uniqueid", "L")])));
        assert!(!out.iter().any(|o| o.get("kind") == "warn"), "실측으로 확인한 정상 흐름이다");
        assert!(has(&out, "call_end", "outcome", "abandoned"));
        let a = e.agent(AGENT).unwrap();
        assert_eq!((a.state, a.missed), (AgentState::Ready, 0));
    }

    #[test]
    fn not_ready_pressed_during_call_applies_after_wrap_up() {
        let mut e = Engine::new(Policy::default());
        connected(&mut e);
        e.apply(7_000_000, &cmd(CommandKind::NotReady, "식사"));
        assert_eq!(e.agent(AGENT).unwrap().state, AgentState::Talking, "통화는 끊지 않는다");
        e.apply(8_000_000, &ev("AgentComplete", &[("Interface", AGENT), ("Reason", "caller")]));
        let out = e.flush();
        let a = e.agent(AGENT).unwrap();
        assert_eq!((a.state, a.reason.as_str()), (AgentState::NotReady, "식사"));
        assert!(has(&out, "action", "reason", "식사"));
    }

    #[test]
    fn repeated_no_answer_sends_agent_to_not_ready() {
        let mut e = Engine::new(Policy::default());
        e.apply(1, &ev("QueueMemberAdded", &[("Interface", AGENT), ("Paused", "0")]));
        for i in 0..2 {
            e.apply(10 + i * 2, &ev("AgentCalled", &[("Interface", AGENT), ("Linkedid", "L")]));
            e.apply(11 + i * 2, &ev("AgentRingNoAnswer", &[("Interface", AGENT)]));
        }
        let a = e.agent(AGENT).unwrap();
        assert_eq!((a.state, a.reason.as_str()), (AgentState::NotReady, REASON_NO_ANSWER));
    }

    #[test]
    fn advance_ends_wrap_up_without_input() {
        let mut e = Engine::new(Policy::default());
        connected(&mut e);
        e.apply(8_000_000, &ev("AgentComplete", &[("Interface", AGENT), ("Reason", "caller")]));
        assert!(e.advance(17_999_999).is_empty(), "후처리 10초가 아직 안 지났다");
        let out = e.advance(18_000_000);
        assert!(has(&out, "action", "action", "QueuePause"));
        assert_eq!(e.agent(AGENT).unwrap().state, AgentState::Ready);
    }

    fn msg(kv: &[(&str, &str)]) -> Message {
        Message { fields: kv.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect() }
    }

    #[test]
    fn resync_corrects_channels_and_pause() {
        let mut e = Engine::new(Policy::default());
        e.apply(1, &ev("QueueMemberAdded", &[("Interface", AGENT), ("Paused", "0")]));
        let out = e.apply(2, &cmd(CommandKind::NotReady, "교육"));
        assert!(has(&out, "action", "reason", "교육"), "대기 → 이석 명령은 QueuePause(true, 사유)");
        e.apply(3, &ev("Newchannel", &[("Uniqueid", "X"), ("Linkedid", "X")]));

        // 끊긴 동안 X는 끝났고 Y가 생겼다
        let snap = vec![msg(&[("Event", "CoreShowChannel"), ("Uniqueid", "Y"), ("Linkedid", "Y")])];
        let out = e.apply(4, &Input::ChannelSnapshot(snap));
        assert!(has(&out, "warn", "code", "resync_lost_channel") && has(&out, "call_end", "id", "X"));
        assert!(has(&out, "warn", "code", "resync_new_channel") && has(&out, "call", "id", "Y"));

        // Asterisk는 대기(Paused=0)로 알고 있다 → 엔진 상태(이석)대로 다시 보낸다
        let snap = vec![msg(&[("Event", "QueueMember"), ("Location", AGENT), ("Name", "SIM-1001"), ("Paused", "0")])];
        let out = e.apply(5, &Input::QueueSnapshot(snap));
        assert!(has(&out, "warn", "code", "resync_pause") && has(&out, "action", "reason", "교육"));
    }

    #[test]
    fn resync_re_adds_member_lost_by_asterisk_restart() {
        let mut e = Engine::new(Policy::default());
        e.apply(1, &ev("QueueMemberAdded", &[("Interface", AGENT), ("Paused", "0")]));
        let out = e.apply(2, &Input::QueueSnapshot(vec![]));
        assert!(has(&out, "warn", "code", "resync_readd"));
        assert!(has(&out, "action", "action", "QueueAdd"));
    }
}

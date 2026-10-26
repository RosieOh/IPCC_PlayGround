"use client";

import { useEffect, useRef, useState } from "react";
import { initialRoomBoard, reduceRooms, type Line, type RoomBoard as Board, type StateView } from "@/lib/rq";
import { useEventStream, useNow } from "@/lib/use-engine";
import { LoginForm } from "./agents";
import { PageHeader } from "./header";
import { IconActivity, IconHeadset, IconOverview, IconRooms } from "./icons";
import { DOCS, Rail, type RailItem } from "./rail";
import { RQ_COMMANDS, RoomAgents } from "./rq-agents";
import { RoomEventLog } from "./rq-event-log";
import { EndedRooms, LiveRooms, ModePhaseMatrix, RoomOverview } from "./rq-rooms";
import { World } from "./world";

const SECTIONS: RailItem[] = [
  { id: "rq-overview", label: "큐 현황 · 두 축", icon: IconOverview },
  { id: "rq-agents", label: "상담원", icon: IconHeadset },
  { id: "rq-rooms", label: "회의방", icon: IconRooms },
  { id: "rq-events", label: "이벤트 로그", icon: IconActivity },
];

/**
 * 룸 큐 현황판 (docs/12). 셸은 app_queue 현황판(board.tsx)과 같고, 내용이 다르다:
 * 상담원은 업무 모드 × 통화 단계 두 축, 콜은 고객마다 하나씩 열리는 회의방, 명령에 보류·협의 전환·스킬이 있다.
 */
export function RoomBoard() {
  const board = useEventStream<Board, StateView, Line>("/api/rq/events", reduceRooms, initialRoomBoard);
  const now = useNow();
  const main = useRef<HTMLElement>(null);
  const [active, setActive] = useState(SECTIONS[0].id);

  // 스크롤 위치에 따라 레일의 현재 메뉴를 바꾼다 (board.tsx와 같은 방식)
  useEffect(() => {
    const root = main.current;
    if (!root) return;
    const visible = new Set<string>();
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) visible.add(e.target.id);
          else visible.delete(e.target.id);
        }
        const first = SECTIONS.find((s) => visible.has(s.id));
        if (first) setActive(first.id);
      },
      { root, rootMargin: "0px 0px -60% 0px" },
    );
    SECTIONS.forEach((s) => {
      const el = document.getElementById(s.id);
      if (el) io.observe(el);
    });
    return () => io.disconnect();
  }, []);

  return (
    <div className="app">
      <World />
      <Rail
        items={SECTIONS}
        active={active}
        doc={{ href: `${DOCS}/12-room-queue.md`, label: "룸 큐 문서 (docs/12)" }}
        other={{ href: "/", label: "app_queue 현황판 (Stage 2 엔진)" }}
      />
      <div className="content">
        <PageHeader
          title="룸 큐 현황판"
          sub={
            <>
              app_queue 없이 엔진(<span className="mono">bin/rqengine</span>)이 분배해요 · 고객마다 회의방 하나 · support 5100 · sales 5101
            </>
          }
          state={board}
          now={now}
        />
        <main className="main" ref={main}>
          <div className="page">
            {board.link === "engine-down" && (
              <div className="alert tone-rose" role="alert">
                <b>룸 큐 엔진에 연결할 수 없어요.</b>
                <span>
                  다른 터미널에서 <span className="kbd">make up</span> → <span className="kbd">make rq-engine</span>을 실행하면 3초 안에 다시
                  붙어요.
                </span>
              </div>
            )}
            {board.link === "live" && !board.asterisk && (
              <div className="alert tone-amber" role="status">
                <b>엔진이 Asterisk(AMI)에 붙지 못했어요.</b>
                <span>
                  <span className="kbd">make up</span>으로 Asterisk를 띄우면 엔진이 다시 붙고, 채널 목록으로 진행 중인 방을 다시 맞춰요.
                </span>
              </div>
            )}

            <section id="rq-overview" className="section">
              <div className="section-head">
                <h2 className="section-title">큐 현황</h2>
                <span className="section-desc">
                  벨 {Math.round(board.ringMs / 1000)}초 · 후처리 {Math.round(board.acwMs / 1000)}초 · 녹음 {board.record ? "켬" : "끔"}
                </span>
              </div>
              <RoomOverview board={board} now={now} />
              <ModePhaseMatrix board={board} />
            </section>

            <section id="rq-agents" className="section">
              <div className="section-head">
                <div>
                  <h2 className="section-title">상담원</h2>
                  <p className="section-desc">
                    업무 모드는 상담원이 정하고, 통화 단계는 교환기에서 일어난 일로 바뀌어요. 콜은 대기 · 통화 없음인 상담원에게만 가요.
                  </p>
                </div>
                <LoginForm endpoint={RQ_COMMANDS} />
              </div>
              <RoomAgents board={board} now={now} />
            </section>

            <div id="rq-rooms" className="grid min-w-0 grid-cols-1 gap-8 scroll-mt-4 xl:grid-cols-2">
              <section className="section">
                <div className="section-head">
                  <h2 className="section-title">진행 중인 방</h2>
                  <span className="section-desc">{Object.keys(board.rooms).length}개 · 보류 중인 고객은 따로 보류 방에 있어요</span>
                </div>
                <LiveRooms board={board} now={now} />
              </section>
              <section className="section">
                <div className="section-head">
                  <h2 className="section-title">최근 종료</h2>
                  <span className="section-desc">결과는 엔진의 call_end 줄</span>
                </div>
                <EndedRooms board={board} />
              </section>
            </div>

            <section id="rq-events" className="section">
              <div className="section-head">
                <div>
                  <h2 className="section-title">이벤트 로그</h2>
                  <p className="section-desc">
                    엔진이 낸 줄 그대로예요 (<span className="mono">bin/rqengine</span> 표준출력과 같아요). 액션은 엔진이 Asterisk에 보낸 AMI 액션이에요.
                  </p>
                </div>
              </div>
              <RoomEventLog board={board} />
            </section>
          </div>
        </main>
      </div>
    </div>
  );
}

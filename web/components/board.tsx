"use client";

import { useEffect, useRef, useState } from "react";
import { useEngine, useNow } from "@/lib/use-engine";
import { AgentBoard, LoginForm } from "./agents";
import { EndedCalls, LiveCalls } from "./calls";
import { EventLog } from "./event-log";
import { PageHeader } from "./header";
import { IconActivity, IconHeadset, IconOverview, IconPhone } from "./icons";
import { Overview } from "./overview";
import { Rail, type RailItem } from "./rail";
import { World } from "./world";

const SECTIONS: RailItem[] = [
  { id: "overview", label: "큐 현황", icon: IconOverview },
  { id: "agents", label: "상담원", icon: IconHeadset },
  { id: "calls", label: "콜", icon: IconPhone },
  { id: "events", label: "이벤트 로그", icon: IconActivity },
];

/**
 * 실시간 현황판 한 화면.
 * 셸: 떠 있는 아이콘 레일 | 페이지 헤더 + 스크롤 본문(섹션: 요약 카드 → 상담원 카드 → 콜 표 2열 → 이벤트 로그)
 */
export function Board() {
  const board = useEngine();
  const now = useNow();
  const main = useRef<HTMLElement>(null);
  const [active, setActive] = useState("overview");

  // 스크롤 위치에 따라 레일의 현재 메뉴를 바꾼다: 본문 위쪽 40% 안에 걸친 섹션 중 맨 위.
  // IntersectionObserver는 상태가 '바뀐' 섹션만 알려 주므로 보이는 섹션 집합을 따로 들고 있는다
  // (경고 배너가 사라져 레이아웃이 밀릴 때 계속 보이던 섹션이 빠지지 않게).
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
      <Rail items={SECTIONS} active={active} />
      <div className="content">
        <PageHeader board={board} now={now} />
        <main className="main" ref={main}>
          <div className="page">
            {board.link === "engine-down" && (
              <div className="alert tone-rose" role="alert">
                <b>엔진에 연결할 수 없어요.</b>
                <span>
                  다른 터미널에서 <span className="kbd">make up</span> → <span className="kbd">make engine</span>을 실행하면 3초 안에
                  다시 붙어요.
                </span>
              </div>
            )}
            {board.link === "live" && !board.asterisk && (
              <div className="alert tone-amber" role="status">
                <b>엔진이 Asterisk(AMI)에 붙지 못했어요.</b>
                <span>
                  <span className="kbd">make up</span>으로 Asterisk를 띄우면 엔진이 알아서 다시 붙고 재동기화해요.
                </span>
              </div>
            )}

            <section id="overview" className="section">
              <div className="section-head">
                <h2 className="section-title">큐 현황</h2>
                <span className="section-desc">{board.queue} 큐 · 처리 결과는 엔진 시작 이후 누적</span>
              </div>
              <Overview board={board} now={now} />
            </section>

            <section id="agents" className="section">
              <div className="section-head">
                <div>
                  <h2 className="section-title">상담원</h2>
                  <p className="section-desc">
                    상태는 엔진이 정하고 Asterisk 큐에는 QueuePause로 맞춰요. 후처리는 {Math.round(board.acwMs / 1000)}초 뒤 자동으로 끝나요.
                  </p>
                </div>
                <LoginForm />
              </div>
              <AgentBoard board={board} now={now} />
            </section>

            <div id="calls" className="grid min-w-0 grid-cols-1 gap-8 scroll-mt-4 xl:grid-cols-2">
              <section className="section">
                <div className="section-head">
                  <h2 className="section-title">진행 중인 콜</h2>
                  <span className="section-desc">{Object.keys(board.calls).length}건</span>
                </div>
                <LiveCalls board={board} now={now} />
              </section>
              <section className="section">
                <div className="section-head">
                  <h2 className="section-title">최근 종료</h2>
                  <span className="section-desc">결과는 상태머신의 call_end 줄</span>
                </div>
                <EndedCalls board={board} />
              </section>
            </div>

            <section id="events" className="section">
              <div className="section-head">
                <div>
                  <h2 className="section-title">이벤트 로그</h2>
                  <p className="section-desc">
                    엔진이 낸 줄 그대로예요 (<span className="mono">bin/state replay</span>와 같은 형식). 액션은 엔진이 Asterisk에 보낸 명령이에요.
                  </p>
                </div>
              </div>
              <EventLog board={board} />
            </section>
          </div>
        </main>
      </div>
    </div>
  );
}

"use client";

import { useEffect, useReducer, useSyncExternalStore } from "react";
import { initialBoard, reduce, type Line, type StateView } from "./engine";

/**
 * 엔진 상태를 구독한다. 게이트웨이(/api/engine/events)를 거쳐 엔진의 SSE에 붙는다.
 * 접속(재접속 포함)하면 "state" 이벤트로 전체 상태를 받고, 이후에는 바뀐 줄만 받는다.
 * EventSource가 끊기면 브라우저가 retry 간격(3초)마다 알아서 다시 붙는다.
 */
export function useEngine() {
  const [board, dispatch] = useReducer(reduce, initialBoard);

  useEffect(() => {
    const es = new EventSource("/api/engine/events");
    es.addEventListener("state", (e) => {
      dispatch({ type: "state", view: JSON.parse((e as MessageEvent<string>).data) as StateView, receivedAt: Date.now() });
    });
    es.addEventListener("engine-down", () => dispatch({ type: "link", link: "engine-down" }));
    es.onmessage = (e: MessageEvent<string>) => dispatch({ type: "line", line: JSON.parse(e.data) as Line });
    es.onerror = () => dispatch({ type: "link", link: "connecting" });
    return () => es.close();
  }, []);

  return board;
}

/** 상담원 명령을 엔진에 보낸다. 결과(상태 변화)는 SSE로 돌아온다. */
export async function sendCommand(kind: string, iface: string, arg = ""): Promise<void> {
  const res = await fetch("/api/engine/commands", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind, iface, arg }),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `HTTP ${res.status}`);
  }
}

// ── 1초 시계 ──────────────────────────────────────────────────────────────
// 경과 시간 표시용. 렌더 중에 Date.now()를 부르지 않도록 외부 저장소로 둔다 (서버 렌더에서는 0 = 모름).

let clock = 0;
let timer: ReturnType<typeof setInterval> | undefined;
const listeners = new Set<() => void>();

function subscribe(cb: () => void) {
  listeners.add(cb);
  if (!timer) {
    clock = Date.now();
    timer = setInterval(() => {
      clock = Date.now();
      listeners.forEach((l) => l());
    }, 1000);
  }
  return () => {
    listeners.delete(cb);
    if (listeners.size === 0 && timer) {
      clearInterval(timer);
      timer = undefined;
    }
  };
}

export function useNow(): number {
  return useSyncExternalStore(
    subscribe,
    () => clock,
    () => 0,
  );
}

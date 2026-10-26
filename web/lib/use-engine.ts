"use client";

import { useEffect, useReducer, useSyncExternalStore } from "react";
import { initialBoard, reduce, type Board, type Line, type StateView } from "./engine";

/** 엔진 SSE를 화면 상태로 접는 리듀서가 받는 동작. 두 현황판(app_queue · 룸 큐)이 같은 모양을 쓴다. */
export type StreamAction<V, L> =
  | { type: "state"; view: V; receivedAt: number }
  | { type: "line"; line: L }
  | { type: "link"; link: "connecting" | "live" | "engine-down" };

/**
 * 엔진 SSE를 구독한다 (게이트웨이 url을 거쳐 엔진의 /api/events에 붙는다).
 * 접속(재접속 포함)하면 "state" 이벤트로 전체 상태를 받고, 이후에는 바뀐 줄만 받는다.
 * EventSource가 끊기면 브라우저가 retry 간격(3초)마다 알아서 다시 붙는다.
 */
export function useEventStream<B, V, L>(url: string, reducer: (b: B, a: StreamAction<V, L>) => B, initial: B): B {
  const [board, dispatch] = useReducer(reducer, initial);

  useEffect(() => {
    const es = new EventSource(url);
    es.addEventListener("state", (e) => {
      dispatch({ type: "state", view: JSON.parse((e as MessageEvent<string>).data) as V, receivedAt: Date.now() });
    });
    es.addEventListener("engine-down", () => dispatch({ type: "link", link: "engine-down" }));
    es.onmessage = (e: MessageEvent<string>) => dispatch({ type: "line", line: JSON.parse(e.data) as L });
    es.onerror = () => dispatch({ type: "link", link: "connecting" });
    return () => es.close();
  }, [url]);

  return board;
}

/** 상태머신 엔진(bin/engine)을 구독한다. */
export function useEngine() {
  return useEventStream<Board, StateView, Line>("/api/engine/events", reduce, initialBoard);
}

/** 명령을 엔진에 보낸다. 결과(상태 변화)는 SSE로 돌아온다. endpoint는 게이트웨이 경로다. */
export async function sendCommand(kind: string, iface: string, arg = "", endpoint = "/api/engine/commands"): Promise<void> {
  const res = await fetch(endpoint, {
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

import { ENGINE_URL, proxyEvents } from "@/lib/server";

// 게이트웨이: 브라우저의 EventSource를 엔진의 SSE(/api/events)에 그대로 이어 준다 (lib/server.ts proxyEvents).

export async function GET(req: Request) {
  return proxyEvents(req, ENGINE_URL);
}

import { proxyEvents, RQ_ENGINE_URL } from "@/lib/server";

// 게이트웨이: 룸 큐 화면의 EventSource를 룸 큐 엔진(bin/rqengine -http)의 SSE에 이어 준다.

export async function GET(req: Request) {
  return proxyEvents(req, RQ_ENGINE_URL);
}

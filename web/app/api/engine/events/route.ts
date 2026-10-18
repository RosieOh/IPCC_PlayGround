import { ENGINE_URL, sseHeaders } from "@/lib/server";

// 게이트웨이: 브라우저의 EventSource를 엔진의 SSE(/api/events)에 그대로 이어 준다.
// 엔진이 꺼져 있으면 engine-down 이벤트 하나를 보내고 끊는다 → EventSource가 retry(3초)마다 다시 붙는다.
// 브라우저가 떠나면 req.signal이 끊겨 엔진 쪽 구독도 끝난다.

export async function GET(req: Request) {
  let upstream: Response | undefined;
  try {
    upstream = await fetch(`${ENGINE_URL}/api/events`, {
      signal: req.signal,
      cache: "no-store",
      headers: { Accept: "text/event-stream" },
    });
  } catch {
    upstream = undefined;
  }
  if (!upstream?.ok || !upstream.body) {
    const body = `retry: 3000\nevent: engine-down\ndata: ${JSON.stringify({ engine: ENGINE_URL })}\n\n`;
    return new Response(body, { headers: sseHeaders });
  }
  return new Response(upstream.body, { headers: sseHeaders });
}

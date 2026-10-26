// 게이트웨이(라우트 핸들러) 공용 설정. 브라우저는 엔진 주소를 모르고 이 서버만 부른다.

/** 엔진(bin/engine -http) 주소. 기본은 같은 Mac의 127.0.0.1:8090 */
export const ENGINE_URL = (process.env.ENGINE_URL ?? "http://127.0.0.1:8090").replace(/\/$/, "");

/** 룸 큐 엔진(bin/rqengine -http) 주소. 기본은 127.0.0.1:8091 */
export const RQ_ENGINE_URL = (process.env.RQ_ENGINE_URL ?? "http://127.0.0.1:8091").replace(/\/$/, "");

export const sseHeaders = {
  "Content-Type": "text/event-stream; charset=utf-8",
  // no-transform: next start의 압축이 스트림을 모아 두지 않게
  "Cache-Control": "no-cache, no-transform",
  "X-Accel-Buffering": "no",
};

/**
 * 브라우저의 EventSource를 엔진의 SSE(/api/events)에 그대로 이어 준다.
 * 엔진이 꺼져 있으면 engine-down 이벤트 하나를 보내고 끊는다 → EventSource가 retry(3초)마다 다시 붙는다.
 * 브라우저가 떠나면 req.signal이 끊겨 엔진 쪽 구독도 끝난다.
 */
export async function proxyEvents(req: Request, engine: string): Promise<Response> {
  let upstream: Response | undefined;
  try {
    upstream = await fetch(`${engine}/api/events`, {
      signal: req.signal,
      cache: "no-store",
      headers: { Accept: "text/event-stream" },
    });
  } catch {
    upstream = undefined;
  }
  if (!upstream?.ok || !upstream.body) {
    const body = `retry: 3000\nevent: engine-down\ndata: ${JSON.stringify({ engine })}\n\n`;
    return new Response(body, { headers: sseHeaders });
  }
  return new Response(upstream.body, { headers: sseHeaders });
}

/** 명령을 엔진(/api/commands)에 넘긴다. 검사는 엔진이 한다. */
export async function proxyCommand(req: Request, engine: string): Promise<Response> {
  const body = await req.text();
  try {
    const res = await fetch(`${engine}/api/commands`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body,
      cache: "no-store",
    });
    return new Response(await res.text(), {
      status: res.status,
      headers: { "Content-Type": "application/json; charset=utf-8" },
    });
  } catch {
    return Response.json({ error: `엔진(${engine})에 연결할 수 없어요` }, { status: 502 });
  }
}

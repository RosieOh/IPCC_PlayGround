import { ENGINE_URL } from "@/lib/server";

// 게이트웨이: 상담원 명령을 엔진(/api/commands)에 넘긴다. 검사는 엔진이 한다 (명령 파일과 같은 파서).

export async function POST(req: Request) {
  const body = await req.text();
  try {
    const res = await fetch(`${ENGINE_URL}/api/commands`, {
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
    return Response.json({ error: `엔진(${ENGINE_URL})에 연결할 수 없어요` }, { status: 502 });
  }
}

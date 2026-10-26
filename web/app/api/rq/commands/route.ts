import { proxyCommand, RQ_ENGINE_URL } from "@/lib/server";

// 게이트웨이: 룸 큐 명령(보류·협의 전환·스킬 …)을 룸 큐 엔진에 넘긴다. 검사는 엔진이 한다 (rq.ParseCommand).

export async function POST(req: Request) {
  return proxyCommand(req, RQ_ENGINE_URL);
}

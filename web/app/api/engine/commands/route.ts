import { ENGINE_URL, proxyCommand } from "@/lib/server";

// 게이트웨이: 상담원 명령을 엔진(/api/commands)에 넘긴다. 검사는 엔진이 한다 (명령 파일과 같은 파서).

export async function POST(req: Request) {
  return proxyCommand(req, ENGINE_URL);
}

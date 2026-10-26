import type { Metadata } from "next";
import { RoomBoard } from "@/components/rq-board";

export const metadata: Metadata = {
  title: "룸 큐 현황판",
  description: "app_queue 없이 엔진이 분배하는 룸 큐(bin/rqengine)의 상담원 두 축 상태와 회의방을 실시간으로 보는 현황판",
};

export default function Page() {
  return <RoomBoard />;
}

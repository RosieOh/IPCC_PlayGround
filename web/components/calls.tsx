import { callStateInfo, outcomeInfo, type Board } from "@/lib/engine";
import { fmtClock, fmtDuration, fmtPhone, fmtSeconds, shortId } from "@/lib/format";

const agentName = (b: Board, iface: string) => (iface ? (b.agents[iface]?.name ?? iface) : "—");

/** 진행 중인 콜: 오래된 것부터. 대기 중이면 큐에 들어온 뒤로, 통화 중이면 연결된 뒤로 경과 시간을 센다 */
export function LiveCalls({ board, now }: { board: Board; now: number }) {
  const serverNow = now + board.offsetMs;
  const calls = Object.values(board.calls).sort((a, b) => (a.queuedAt ?? a.since ?? 0) - (b.queuedAt ?? b.since ?? 0));
  return (
    <div className="table-wrap glass">
      <table className="table">
        <thead>
          <tr>
            <th>고객</th>
            <th>상태</th>
            <th>상담원</th>
            <th className="text-right">경과</th>
          </tr>
        </thead>
        <tbody>
          {calls.length === 0 ? (
            <tr>
              <td colSpan={4} className="empty">
                진행 중인 콜이 없어요. <span className="kbd">make sim-call n=3</span>
              </td>
            </tr>
          ) : (
            calls.map((c) => {
              const info = callStateInfo[c.state];
              const waiting = c.state === "Queued" || c.state === "Alerting" || c.state === "Entered";
              const from = waiting ? (c.queuedAt ?? c.since) : c.since;
              return (
                <tr key={c.id}>
                  <td>
                    <div className="td-strong td-num">{c.customer ? fmtPhone(c.customer) : "번호 확인 중"}</div>
                    <div className="mono">{shortId(c.id)}</div>
                  </td>
                  <td>
                    <span className={`badge tone-${info.tone}`}>{info.label}</span>
                  </td>
                  <td>{agentName(board, c.agent)}</td>
                  <td className="td-num text-right">{from && now ? fmtDuration(serverNow - from) : "—"}</td>
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
}

/** 최근 종료한 콜과 결과 (call_end 줄) */
export function EndedCalls({ board }: { board: Board }) {
  return (
    <div className="table-wrap glass">
      <table className="table">
        <thead>
          <tr>
            <th>종료</th>
            <th>고객</th>
            <th>결과</th>
            <th className="text-right">대기</th>
            <th className="text-right">통화</th>
            <th>상담원</th>
          </tr>
        </thead>
        <tbody>
          {board.ended.length === 0 ? (
            <tr>
              <td colSpan={6} className="empty">
                아직 끝난 콜이 없어요
              </td>
            </tr>
          ) : (
            board.ended.map((e) => {
              const info = outcomeInfo(e.outcome);
              return (
                <tr key={`${e.id}-${e.t}`}>
                  <td className="td-num">{fmtClock(parseFloat(e.t) * 1000)}</td>
                  <td className="td-strong td-num">{e.customer ? fmtPhone(e.customer) : "—"}</td>
                  <td>
                    <span className={`badge tone-${info.tone}`}>{info.label}</span>
                  </td>
                  <td className="td-num text-right">{fmtSeconds(e.wait_ms)}</td>
                  <td className="td-num text-right">{e.talk_ms ? fmtSeconds(e.talk_ms) : "—"}</td>
                  <td>{agentName(board, e.agent)}</td>
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
}

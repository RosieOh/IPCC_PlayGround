// 화면 표시용 형식. 시각은 모두 밀리초(epoch)다.

/** 경과 시간: 0:07, 12:34, 1:02:03 */
export function fmtDuration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${sec}` : `${m}:${sec}`;
}

/** 초 단위 짧은 길이: 1.0초, 12초, 2분 5초 */
export function fmtSeconds(ms: number): string {
  if (ms < 10_000) return `${(ms / 1000).toFixed(1)}초`;
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}초` : `${Math.floor(s / 60)}분 ${s % 60}초`;
}

const pad = (n: number, w = 2) => String(n).padStart(w, "0");

/** 시각: 14:03:27 (ms를 주면 .123까지) */
export function fmtClock(epochMs: number, withMs = false): string {
  const d = new Date(epochMs);
  const base = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  return withMs ? `${base}.${pad(d.getMilliseconds(), 3)}` : base;
}

/** 전화번호: 01097920001 → 010-9792-0001. 내선(9001 등)은 그대로 */
export function fmtPhone(n: string): string {
  if (/^01\d{9}$/.test(n)) return `${n.slice(0, 3)}-${n.slice(3, 7)}-${n.slice(7)}`;
  if (/^0\d{9}$/.test(n)) return `${n.slice(0, 3)}-${n.slice(3, 6)}-${n.slice(6)}`;
  return n;
}

/** 콜 ID(Linkedid) 짧게: 1792219444.16 → …444.16 */
export function shortId(id: string): string {
  return id.length > 9 ? `…${id.slice(-6)}` : id;
}

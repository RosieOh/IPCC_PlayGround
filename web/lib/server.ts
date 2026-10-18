// 게이트웨이(라우트 핸들러) 공용 설정. 브라우저는 엔진 주소를 모르고 이 서버만 부른다.

/** 엔진(bin/engine -http) 주소. 기본은 같은 Mac의 127.0.0.1:8090 */
export const ENGINE_URL = (process.env.ENGINE_URL ?? "http://127.0.0.1:8090").replace(/\/$/, "");

export const sseHeaders = {
  "Content-Type": "text/event-stream; charset=utf-8",
  // no-transform: next start의 압축이 스트림을 모아 두지 않게
  "Cache-Control": "no-cache, no-transform",
  "X-Accel-Buffering": "no",
};

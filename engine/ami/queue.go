package ami

import "sync/atomic"

// pump는 in에서 받은 값을 순서대로 out으로 넘기되, 생산자(in 쪽)를 절대 막지 않는다.
// 소비자가 느리면 내부 슬라이스에 쌓아 둔다(무제한 큐).
//
// 왜 필요한가: AMI 읽기 루프가 느린 소비자 때문에 멈추면 TCP 버퍼가 차고,
// Asterisk는 manager.conf의 writetimeout 안에 쓰지 못한 이벤트를 잃거나 세션을 정리한다.
// CTI 엔진에서 이벤트 유실은 '유령 통화' 같은 상태 오류로 이어지므로
// 메모리를 조금 더 쓰더라도 읽기 루프를 막지 않는 쪽을 택했다. 대신 depth로 적체를 감시한다.
//
// done이 닫히면 남은 값을 버리고 끝낸다. in이 닫히면 남은 값을 마저 넘기고 out을 닫는다.
// onDepth가 있으면 깊이가 바뀔 때마다 새 깊이로 부른다 (적체 경고용, pump 고루틴에서만 불린다).
func pump[T any](in <-chan T, out chan<- T, depth *atomic.Int64, done <-chan struct{}, onDepth func(int64)) {
	defer close(out)
	changed := func(d int64) {
		if onDepth != nil {
			onDepth(d)
		}
	}
	var q []T
	for {
		// 보낼 게 없으면 send를 nil로 두어 select에서 그 case가 선택되지 않게 한다.
		var send chan<- T
		var next T
		if len(q) > 0 {
			send = out
			next = q[0]
		}

		select {
		case v, ok := <-in:
			if !ok {
				for _, v := range q {
					select {
					case out <- v:
						changed(depth.Add(-1))
					case <-done:
						return
					}
				}
				return
			}
			q = append(q, v)
			changed(depth.Add(1))
		case send <- next:
			var zero T
			q[0] = zero // 넘긴 값을 GC가 수거할 수 있게
			q = q[1:]
			if len(q) == 0 {
				q = nil
			}
			changed(depth.Add(-1))
		case <-done:
			return
		}
	}
}

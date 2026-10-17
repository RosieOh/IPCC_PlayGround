package ami

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// 소비자가 하나도 읽지 않아도 생산자는 끝까지 보낼 수 있어야 하고, 순서는 지켜져야 한다.
func TestPumpNeverBlocksProducer(t *testing.T) {
	in := make(chan int)
	out := make(chan int)
	done := make(chan struct{})
	defer close(done)
	var depth atomic.Int64
	go pump(in, out, &depth, done, nil)

	const n = 10000
	sent := make(chan struct{})
	go func() {
		for i := range n {
			in <- i
		}
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(testTimeout):
		t.Fatal("producer blocked while nobody was reading")
	}
	eventually(t, func() bool { return depth.Load() == n })

	for i := range n {
		if v := <-out; v != i {
			t.Fatalf("out[%d] = %d", i, v)
		}
	}
	eventually(t, func() bool { return depth.Load() == 0 })
}

func TestPumpFlushesAndClosesWhenInputCloses(t *testing.T) {
	in := make(chan int)
	out := make(chan int)
	var depth atomic.Int64
	go pump(in, out, &depth, make(chan struct{}), nil)

	for i := range 3 {
		in <- i
	}
	close(in)
	var got []int
	for v := range out {
		got = append(got, v)
	}
	if !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("got %v", got)
	}
}

// eventually는 cond가 참이 될 때까지 잠깐씩 기다린다.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

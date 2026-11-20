package main

import (
	"sync/atomic"
	"testing"
	"time"

	"portide/app"
)

// 退出收尾在主线程上执行。转发器这时可能正在刷新托盘、等着主线程空出来：收尾不能再等它，否则两边互相等待
// （Ctrl+C 或托盘"退出"后应用无响应）。开始退出之后的信号也不再转发。
func TestQuitDoesNotWaitForAForwardThatNeedsTheMainThread(t *testing.T) {
	var gate quitGate
	mainFree := make(chan struct{}) // 收尾返回后"主线程"才空出来
	entered := make(chan struct{}, 1)
	var forwarded atomic.Int32
	changes := app.NewCoalescer(time.Millisecond, gate.wrap(func(string) {
		forwarded.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-mainFree // 刷新托盘要回到主线程
	}))
	changes.Signal("devices")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("the first signal was not forwarded")
	}

	done := make(chan struct{})
	go func() { // 这个 goroutine 扮演主线程上的收尾
		gate.begin(changes.Close)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("quitting waited for a forward that needs the main thread")
	}
	close(mainFree)

	changes.Signal("transfers")
	time.Sleep(20 * time.Millisecond)
	if n := forwarded.Load(); n != 1 {
		t.Fatalf("forwarded %d signals, want only the one before quitting", n)
	}
}

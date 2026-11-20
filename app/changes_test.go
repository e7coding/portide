package app

import (
	"slices"
	"sync"
	"testing"
	"time"
)

type emitted struct {
	mu     sync.Mutex
	topics []string
}

func (e *emitted) add(topic string) {
	e.mu.Lock()
	e.topics = append(e.topics, topic)
	e.mu.Unlock()
}

func (e *emitted) count(topic string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, t := range e.topics {
		if t == topic {
			n++
		}
	}
	return n
}

// 同一主题的一串信号：第一次立即转发，其余合并成窗口结束时的一次；不同主题互不影响。
func TestCoalescerMergesBursts(t *testing.T) {
	var e emitted
	c := NewCoalescer(100*time.Millisecond, e.add)
	defer c.Close()

	for range 20 {
		c.Signal("transfers")
	}
	c.Signal("devices")
	eventually(t, "first signals forwarded", func() bool { return e.count("transfers") == 1 && e.count("devices") == 1 })
	eventually(t, "the burst forwarded once more", func() bool { return e.count("transfers") == 2 })
	time.Sleep(250 * time.Millisecond)
	if got := e.count("transfers"); got != 2 {
		t.Fatalf("transfers forwarded %d times, want 2", got)
	}
	if got := e.count("devices"); got != 1 {
		t.Fatalf("devices forwarded %d times, want 1", got)
	}

	// 安静下来之后的新信号又是立即转发。
	c.Signal("transfers")
	eventually(t, "a later signal forwarded at once", func() bool { return e.count("transfers") == 3 })
}

// 关闭之后不再转发，Signal 也不会阻塞。
func TestCoalescerClose(t *testing.T) {
	var e emitted
	c := NewCoalescer(time.Hour, e.add)
	c.Signal("history")
	eventually(t, "forwarded", func() bool { return e.count("history") == 1 })
	c.Signal("history") // 约在一小时之后
	c.Close()
	for range 1000 {
		c.Signal("history")
	}
	if slices.Contains(e.topics[1:], "history") {
		t.Fatal("nothing must be forwarded after Close")
	}
}

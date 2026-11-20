package app

import (
	"sync"
	"time"
)

// ChangeWindow 是同一主题两次转发之间的最短间隔：传输进行中进度会频繁写库，界面不需要每次都刷新。
const ChangeWindow = 200 * time.Millisecond

// Coalescer 把核心的变化信号按主题合并后转发给界面（M3a 设计 2.1）：一个主题在空闲时立即转发；
// 距上次转发不到 window 时，这段时间里的信号合并成一次，到时间再转发。emit 在 Coalescer 自己的 goroutine 里调用。
type Coalescer struct {
	window time.Duration
	emit   func(topic string)
	in     chan string
	flush  chan string
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

// NewCoalescer 创建并启动合并器。用完要调用 Close。
func NewCoalescer(window time.Duration, emit func(topic string)) *Coalescer {
	c := &Coalescer{
		window: window, emit: emit,
		in: make(chan string, 256), flush: make(chan string, 16),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go c.loop()
	return c
}

// Signal 记下一个主题。它满足核心对变化回调的约束：从不阻塞（通道满了就丢掉这一次，
// 界面每 3 秒的兜底刷新会补上）。
func (c *Coalescer) Signal(topic string) {
	select {
	case c.in <- topic:
	default:
	}
}

// Close 停止合并器；还没到时间转发的信号直接丢弃。
func (c *Coalescer) Close() {
	c.once.Do(func() { close(c.stop) })
	<-c.done
}

func (c *Coalescer) loop() {
	defer close(c.done)
	last := map[string]time.Time{}
	pending := map[string]*time.Timer{}
	defer func() {
		for _, t := range pending {
			t.Stop()
		}
	}()
	for {
		select {
		case <-c.stop:
			return
		case topic := <-c.in:
			if pending[topic] != nil {
				continue // 已经约好了下一次转发
			}
			if wait := c.window - time.Since(last[topic]); wait > 0 {
				pending[topic] = time.AfterFunc(wait, func() {
					select {
					case c.flush <- topic:
					case <-c.stop:
					}
				})
				continue
			}
			last[topic] = time.Now()
			c.emit(topic)
		case topic := <-c.flush:
			delete(pending, topic)
			last[topic] = time.Now()
			c.emit(topic)
		}
	}
}

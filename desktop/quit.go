package main

import "sync/atomic"

// quitGate 管退出时的变化信号转发。退出收尾在主线程上执行（Ctrl+C、托盘"退出"都一样），
// 而转发器刷新托盘时要回到主线程：收尾如果同步等转发器停下，两边会互相等待，应用无响应、退不出去。
// 所以开始退出后不再转发（要退出了，刷新界面和托盘也没有意义），转发器在后台关闭，收尾不等它。
type quitGate struct {
	quitting atomic.Bool
}

// wrap 包一层转发函数：开始退出之后收到的信号直接丢掉。
func (g *quitGate) wrap(forward func(topic string)) func(topic string) {
	return func(topic string) {
		if !g.quitting.Load() {
			forward(topic)
		}
	}
}

// begin 标记开始退出，并在后台关闭转发器（不等：它可能正等着主线程）。
func (g *quitGate) begin(closeForwarder func()) {
	g.quitting.Store(true)
	go closeForwarder()
}

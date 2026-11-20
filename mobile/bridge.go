package mobile

import (
	"context"
	"fmt"
	"sync"

	"portide/core/discovery"
)

// bridge 把核心的 discovery.Discoverer 接到 Swift 的 Bonjour（M4 设计 3.2）：每次 Announce / Browse 分配一个编号，
// 调 Platform 开始，ctx 结束时调对应的停止；Swift 解析出的实例经 App.ReportFound 按编号交回给 Browse 的回调。
type bridge struct {
	p Platform

	mu       sync.Mutex
	next     int64
	browsing map[int64]*browse
}

// browse 是一个进行中的浏览。mu 保证 Browse 返回之后不会再调用 fn（discovery.Discoverer 的约定）。
type browse struct {
	mu     sync.Mutex
	fn     func(discovery.Found)
	closed bool
}

func newBridge(p Platform) *bridge {
	return &bridge{p: p, browsing: map[int64]*browse{}}
}

func (b *bridge) id() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	return b.next
}

func (b *bridge) Announce(ctx context.Context, service, instance string, port int) error {
	id := b.id()
	b.p.StartAnnounce(id, service, instance, int64(port))
	<-ctx.Done()
	b.p.StopAnnounce(id)
	return nil
}

func (b *bridge) Browse(ctx context.Context, service string, found func(discovery.Found)) error {
	id := b.id()
	br := &browse{fn: found}
	b.mu.Lock()
	b.browsing[id] = br
	b.mu.Unlock()
	b.p.StartBrowse(id, service)
	<-ctx.Done()
	b.p.StopBrowse(id)
	b.mu.Lock()
	delete(b.browsing, id)
	b.mu.Unlock()
	br.mu.Lock()
	br.closed = true
	br.mu.Unlock()
	return nil
}

// found 把 Swift 报告的一个实例交给对应的浏览；浏览已经结束或者没有可用地址时丢掉。
func (b *bridge) found(id int64, instance string, ips []string, port int) {
	b.mu.Lock()
	br := b.browsing[id]
	b.mu.Unlock()
	if br == nil {
		return
	}
	addrs := orderAddrs(ips, port)
	if len(addrs) == 0 {
		return
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if !br.closed {
		br.fn(discovery.Found{Instance: instance, Addrs: addrs})
	}
}

// orderAddrs 排序并过滤 Swift 报告的地址；测试里换成保留回环地址的版本（假"手机"都在本机回环上）。
var orderAddrs = discovery.OrderAddrs

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

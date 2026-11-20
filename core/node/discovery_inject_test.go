package node

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"portide/core/discovery"
)

// memDiscovery 是内存里的 Discoverer：Announce 登记"127.0.0.1:端口"，Browse 定期把登记的实例报给回调。
// 用来验证核心的发现都经过 Options.Discovery（iOS 上换成 Swift 的 Bonjour）。
type memDiscovery struct {
	mu        sync.Mutex
	entries   map[string]map[string]string // service → instance → addr
	announces map[string]int               // service → Announce 调用次数
}

func newMemDiscovery() *memDiscovery {
	return &memDiscovery{entries: map[string]map[string]string{}, announces: map[string]int{}}
}

func (m *memDiscovery) Announce(ctx context.Context, service, instance string, port int) error {
	m.mu.Lock()
	if m.entries[service] == nil {
		m.entries[service] = map[string]string{}
	}
	m.entries[service][instance] = "127.0.0.1:" + strconv.Itoa(port)
	m.announces[service]++
	m.mu.Unlock()
	<-ctx.Done()
	m.mu.Lock()
	delete(m.entries[service], instance)
	m.mu.Unlock()
	return nil
}

func (m *memDiscovery) Browse(ctx context.Context, service string, found func(discovery.Found)) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		m.mu.Lock()
		var fs []discovery.Found
		for inst, addr := range m.entries[service] {
			fs = append(fs, discovery.Found{Instance: inst, Addrs: []string{addr}})
		}
		m.mu.Unlock()
		for _, f := range fs {
			found(f)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (m *memDiscovery) announced(service string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.announces[service]
}

// 注入的发现：加入方不给地址也能找到邀请方，两台设备不靠静态地址也能互相连上。
func TestInjectedDiscovery(t *testing.T) {
	disc := newMemDiscovery()
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	for _, d := range []*testDevice{laptop, desktop} {
		d.n.opts.DisableMDNS = false
		d.n.opts.Discovery = disc
	}
	laptop.start(t)
	code, _ := invite(t, laptop)
	eventually(t, "pairing announced", func() bool { return disc.announced(discovery.PairService) > 0 })
	if _, err := desktop.n.Join(context.Background(), code, nil, false); err != nil {
		t.Fatalf("Join without an address: %v", err)
	}
	desktop.start(t)
	eventually(t, "both online through the injected discovery", func() bool {
		return online(t, laptop, desktop.n.Identity().KeyID) && online(t, desktop, laptop.n.Identity().KeyID)
	})
	if disc.announced(discovery.MemberService) == 0 {
		t.Fatal("members were not announced through the injected discovery")
	}
}

func TestOrderAddrs(t *testing.T) {
	got := discovery.OrderAddrs([]string{"fe80::1", "2001:db8::1", "10.255.0.9", "bogus", "127.0.0.1"}, 7443)
	want := []string{"10.255.0.9:7443", "[2001:db8::1]:7443"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("OrderAddrs = %v, want %v", got, want)
	}
}

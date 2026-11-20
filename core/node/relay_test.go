package node

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/relay"
	"portide/core/relayserver"
	"portide/core/relayset"
	"portide/core/store"
)

// startTestRelay 在回环地址上运行一个中继，返回客户端配置。
func startTestRelay(t *testing.T) relay.Config {
	t.Helper()
	return startTestRelayWith(t, relayserver.Limits{})
}

// startTestRelayWith 同 startTestRelay，但使用指定的限制（例如限速，让经中继的传输慢下来）。
func startTestRelayWith(t *testing.T, limits relayserver.Limits) relay.Config {
	t.Helper()
	cfg, _ := startStoppableRelay(t, limits)
	return cfg
}

// startStoppableRelay 同 startTestRelayWith，另外返回一个停掉中继的函数（可以重复调用；测试结束时也会调用）。
func startStoppableRelay(t *testing.T, limits relayserver.Limits) (relay.Config, func()) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	tokens := relayserver.OpenTokens(filepath.Join(t.TempDir(), "tokens.json"))
	_, token, err := tokens.Add("测试")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &relayserver.Server{Key: priv, Tokens: tokens, Limits: limits, Logf: t.Logf}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, pc); close(done) }()
	stop := func() { cancel(); <-done; pc.Close() }
	t.Cleanup(stop)
	return relay.Config{Addr: pc.LocalAddr().String(), Fingerprint: relay.Fingerprint(pub), Token: token}, stop
}

// startTCPRelay 同 startTestRelay，但同时监听 UDP 和 TCP（TCP 是另一个随机端口，写进 cfg.TCPPort），
// 另外返回服务端，测试可以用它掐断 TCP 连接。
func startTCPRelay(t *testing.T) (relay.Config, *relayserver.Server) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	tokens := relayserver.OpenTokens(filepath.Join(t.TempDir(), "tokens.json"))
	_, token, err := tokens.Add("测试")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &relayserver.Server{Key: priv, Tokens: tokens, Logf: t.Logf}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.ServeAll(ctx, pc, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done; pc.Close() })
	_, tcpPort, _ := net.SplitHostPort(ln.Addr().String())
	return relay.Config{Addr: pc.LocalAddr().String(), TCPPort: tcpPort, Fingerprint: relay.Fingerprint(pub), Token: token}, srv
}

// udpGate 模拟 UDP 被拦：拦着时连中继的 UDP 拨号一直没有回应，直到 ctx 结束或 open 被调用。
type udpGate struct {
	mu     sync.Mutex
	opened chan struct{}
	isOpen bool
}

func newUDPGate() *udpGate { return &udpGate{opened: make(chan struct{})} }

func (g *udpGate) open() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.isOpen {
		g.isOpen = true
		close(g.opened)
	}
}

// wrap 返回经过闸门的拨号：拦着时阻塞，放开后照常拨 real。
func (g *udpGate) wrap(real relay.DialFunc) relay.DialFunc {
	return func(ctx context.Context, addr string, tlsConf *tls.Config) (*quic.Conn, error) {
		select {
		case <-g.opened:
			return real(ctx, addr, tlsConf)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// blockRelayUDP 让 d 连中继和探测 UDP 都经过闸门 g，并把 TCPDelay 调小，免得测试等满 3 秒。启动前调用。
func blockRelayUDP(d *testDevice, g *udpGate) {
	d.n.relayUDPDial = g.wrap
	d.n.relayProbeDial = g.wrap
	d.n.relayTCPDelay = 300 * time.Millisecond
}

// relayClientOf 返回 d 到中继 cfg 的当前客户端（没连上时为 nil）。
func relayClientOf(d *testDevice, cfg relay.Config) *relay.Client {
	rc := relayConnOf(d, cfg)
	if rc == nil {
		return nil
	}
	d.n.daemon.mu.Lock()
	defer d.n.daemon.mu.Unlock()
	return rc.client
}

// relayStatus 返回守护进程记下的某个中继的连接状态。
func relayStatus(d *testDevice, cfg relay.Config) string {
	return d.n.RelayStatuses()[hex.EncodeToString(cfg.Fingerprint[:])]
}

// relayConnOf 返回守护进程里某个中继的连接循环（没有时为 nil）。
func relayConnOf(d *testDevice, cfg relay.Config) *relayConn {
	d.n.daemon.mu.Lock()
	defer d.n.daemon.mu.Unlock()
	return d.n.daemon.relays[cfg.Fingerprint]
}

// onRelay 报告 d 是否认为成员 keyID 在（任意一个）中继上在线。
func onRelay(d *testDevice, keyID string) bool {
	d.n.daemon.mu.Lock()
	defer d.n.daemon.mu.Unlock()
	_, ok := d.n.daemon.relayOnlineSince(keyID)
	return ok
}

// relayedVia 报告 d 到 keyID 的当前连接是否经地址为 addr 的中继。
func relayedVia(d *testDevice, keyID, addr string) bool {
	mc := connPtr(d, keyID)
	return mc != nil && mc.conn.RemoteAddr() == relay.Addr{Relay: addr}
}

// relayOnlyPair 让 a、b 组成设备圈后只能经中继相连：关 mDNS（newDevice 已关）、忘掉对方的地址、不发打洞候选地址。
// 返回时 a 在运行，b 没有启动。
func relayOnlyPair(t *testing.T) (a, b *testDevice) {
	t.Helper()
	a, b = newDevice(t, "笔记本"), newDevice(t, "台式机")
	for _, d := range []*testDevice{a, b} {
		d.n.opts.RelayAfter = 200 * time.Millisecond
		d.n.candidatesHook = noDirect
	}
	a.start(t)
	joinCircle(t, a, b)
	clearAddrs(t, a, b)
	clearAddrs(t, b, a)
	return a, b
}

func useRelay(t *testing.T, d *testDevice, cfg relay.Config) {
	t.Helper()
	if _, err := d.n.AddRelay(cfg); err != nil {
		t.Fatal(err)
	}
}

// 两台设备在局域网里互相找不到（没有 mDNS、没有记下的地址）：经中继连上，排队的任务在对方上线后经中继送达。
func TestQueuedSendDeliveredThroughRelay(t *testing.T) {
	cfg := startTestRelay(t)
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	laptop.n.opts.RelayAfter = 200 * time.Millisecond
	desktop.n.opts.RelayAfter = 200 * time.Millisecond
	laptop.n.candidatesHook, desktop.n.candidatesHook = noDirect, noDirect // 留在中继上：本机回环上打洞必然成功
	laptop.start(t)
	joinCircle(t, laptop, desktop)
	// 忘掉配对时记下的局域网地址：只剩中继这条路。
	if err := desktop.n.Store().UpsertPeer(store.Peer{KeyID: laptop.n.id.KeyID}); err != nil {
		t.Fatal(err)
	}
	useRelay(t, laptop, cfg)
	useRelay(t, desktop, cfg)
	eventually(t, "laptop registered at the relay", func() bool { return relayStatus(laptop, cfg) == "已连接" })

	id, err := laptop.n.QueueSend(desktop.n.id.KeyID, plan(t, "经中继送达", map[string]string{"中继.txt": "跨网的内容"}))
	if err != nil {
		t.Fatal(err)
	}
	desktop.start(t) // 上线：中继通知笔记本，笔记本经中继连接并发送
	waitStatus(t, laptop, id, store.SendDone)
	if got := received(t, desktop, "中继.txt"); got != "跨网的内容" {
		t.Fatalf("received %q", got)
	}
	mc := connPtr(laptop, desktop.n.id.KeyID)
	if mc == nil || !relay.IsRelayed(mc.conn.RemoteAddr()) {
		t.Fatal("the connection must go through the relay")
	}
	devs, _ := laptop.n.Devices()
	for _, d := range devs {
		if d.KeyID == desktop.n.id.KeyID && (!d.Online || d.Addr != "relay:"+cfg.Addr) {
			t.Fatalf("desktop status = %+v", d)
		}
	}
}

// 局域网能连上时不走中继（局域网优先）。
func TestLANPreferredOverRelay(t *testing.T) {
	cfg := startTestRelay(t)
	laptop, desktop := pairOffline(t)
	useRelay(t, laptop, cfg)
	useRelay(t, desktop, cfg)
	desktop.start(t)
	eventually(t, "both online", func() bool {
		return online(t, laptop, desktop.n.id.KeyID) && online(t, desktop, laptop.n.id.KeyID)
	})
	eventually(t, "desktop registered at the relay", func() bool { return relayStatus(desktop, cfg) == "已连接" })
	time.Sleep(defaultRelayAfter + dialInterval) // 中继报告在线之后再等一轮：已有局域网连接，不应再开中继连接
	if mc := connPtr(laptop, desktop.n.id.KeyID); mc == nil || relay.IsRelayed(mc.conn.RemoteAddr()) {
		t.Fatal("a LAN connection must be kept instead of the relay")
	}
}

func TestRelayStatusShowsRejection(t *testing.T) {
	cfg := startTestRelay(t)
	cfg.Token = make([]byte, relay.TokenSize)
	laptop := newDevice(t, "笔记本")
	if err := laptop.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	useRelay(t, laptop, cfg)
	laptop.start(t)
	eventually(t, "rejection shown", func() bool {
		return strings.HasPrefix(relayStatus(laptop, cfg), "连接失败：中继拒绝了登记")
	})
	// 删除配置后不再连接，状态清空。
	if err := laptop.n.RemoveRelay(cfg.Fingerprint); err != nil {
		t.Fatal(err)
	}
	eventually(t, "status cleared", func() bool { return len(laptop.n.RelayStatuses()) == 0 })
}

// 三台设备只经中继连接（局域网地址被忘记、mDNS 关闭）：移除一台后，其余设备断开它的中继连接，
// 并且它再也连不回来（设计方案 3.5 + 4.2：移除对经中继的连接同样生效）。
func TestRemovalDropsRelayedConnection(t *testing.T) {
	cfg := startTestRelay(t)
	laptop, desktop, phone := circleOfThree(t, nil)
	// 台式机和手机之间留在中继上（它们都不发候选地址）；笔记本已经在运行，不改它。
	desktop.n.candidatesHook, phone.n.candidatesHook = noDirect, noDirect
	for _, d := range []*testDevice{laptop, desktop, phone} {
		d.n.opts.RelayAfter = 200 * time.Millisecond
		useRelay(t, d, cfg)
		peers, _ := d.n.Store().Peers()
		for _, p := range peers {
			_ = d.n.Store().UpsertPeer(store.Peer{KeyID: p.KeyID}) // 忘掉记下的局域网地址：只剩中继这条路
		}
	}
	desktop.start(t)
	phone.start(t)
	isRelayed := func(d *testDevice, id string) bool {
		mc := connPtr(d, id)
		return mc != nil && relay.IsRelayed(mc.conn.RemoteAddr())
	}
	eventually(t, "desktop-phone relayed", func() bool {
		return isRelayed(desktop, phone.n.id.KeyID) && isRelayed(phone, desktop.n.id.KeyID)
	})
	if err := laptop.n.RemoveDevice(phone.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "desktop drops the relayed phone", func() bool {
		return connPtr(desktop, phone.n.id.KeyID) == nil
	})
	time.Sleep(3 * time.Second)
	if connPtr(desktop, phone.n.id.KeyID) != nil || connPtr(laptop, phone.n.id.KeyID) != nil {
		t.Fatal("removed phone reconnected over relay")
	}
}

// 配对时邀请方把中继配置交给新设备。
func TestJoinCopiesRelayConfig(t *testing.T) {
	cfg := startTestRelay(t)
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	if err := laptop.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	useRelay(t, laptop, cfg)
	laptop.start(t)
	joinCircle(t, laptop, desktop)
	got, err := desktop.n.Relays()
	if err != nil || len(got) != 1 || got[0].ConfigString() != cfg.ConfigString() {
		t.Fatalf("joiner relay config = %v %v", got, err)
	}
}

// 中继没有回应（服务器防火墙或云安全组没放行 UDP）时，状态要说清楚去查什么，而不是显示 Go 的超时原文。
func TestRelayErrTextExplainsNoResponse(t *testing.T) {
	for _, err := range []error{
		context.DeadlineExceeded,
		fmt.Errorf("dial: %w", context.DeadlineExceeded),
		&quic.HandshakeTimeoutError{},
		&quic.IdleTimeoutError{},
	} {
		got := relayErrText(err, relay.Config{Addr: "203.0.113.7:7443"})
		want := "中继没有回应（检查地址是否正确，以及服务器的防火墙和云服务商的安全组是否放行了 UDP 7443 或 TCP 7443 端口）"
		if got != want {
			t.Errorf("relayErrText(%v) = %q, want %q", err, got, want)
		}
	}
	// 两条路都没回应：TCP 用了别的端口时写实际的 TCP 端口。
	noResp := fmt.Errorf("connect: %w", relay.ErrNoResponse)
	got := relayErrText(noResp, relay.Config{Addr: "203.0.113.7:7443", TCPPort: "443"})
	want := "中继没有回应（检查地址是否正确，以及服务器的防火墙和云服务商的安全组是否放行了 UDP 7443 或 TCP 443 端口）"
	if got != want {
		t.Errorf("relayErrText(ErrNoResponse) = %q, want %q", got, want)
	}
}

// A 有 [R1, R2]，B 只有 [R2]（B 是不认识 relays 的旧版本，A 的列表不会同步给它）：两边经 R2 连上，
// A 的设备列表里 B 的地址带着 R2 的地址。
func TestPeerOnlyOnSecondRelay(t *testing.T) {
	r1, r2 := startTestRelay(t), startTestRelay(t)
	a, b := newDevice(t, "笔记本"), newDevice(t, "台式机")
	b.n.features = slices.DeleteFunc(slices.Clone(features), func(f string) bool { return f == FeatureRelays })
	for _, d := range []*testDevice{a, b} {
		d.n.opts.RelayAfter = 200 * time.Millisecond
		d.n.candidatesHook = noDirect
	}
	a.start(t)
	joinCircle(t, a, b)
	clearAddrs(t, a, b)
	clearAddrs(t, b, a)
	useRelay(t, a, r1)
	useRelay(t, a, r2)
	s, err := relayset.Sign(b.n.id.Private, 1, []relay.Config{r2}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.n.saveRelaySettings(s); err != nil {
		t.Fatal(err)
	}
	b.start(t)
	eventually(t, "connected over R2", func() bool {
		for _, dev := range mustDevices(t, a) {
			if dev.KeyID == b.n.id.KeyID {
				return dev.Online && dev.Addr == "relay:"+r2.Addr
			}
		}
		return false
	})
	if !relayedVia(b, a.n.id.KeyID, r2.Addr) {
		t.Fatal("B must be connected to A over R2")
	}
}

// A、B 都有 [R1, R2]：R1 停掉之后，断开再重连仍然经 R2 连上；状态里 R1 失败、R2 已连接。
func TestOneRelayDownOtherStillWorks(t *testing.T) {
	r1, stop1 := startStoppableRelay(t, relayserver.Limits{})
	r2 := startTestRelay(t)
	a, b := relayOnlyPair(t)
	for _, d := range []*testDevice{a, b} {
		useRelay(t, d, r1)
		useRelay(t, d, r2)
	}
	b.start(t)
	eventually(t, "connected over a relay", func() bool {
		mc := connPtr(a, b.n.id.KeyID)
		return mc != nil && relay.IsRelayed(mc.conn.RemoteAddr())
	})
	stop1()
	eventually(t, "R1 down, R2 connected", func() bool {
		s1 := relayStatus(a, r1)
		return (strings.HasPrefix(s1, "连接失败：") || s1 == "连接已断开，正在重连") && relayStatus(a, r2) == "已连接"
	})
	// 不管刚才走的是哪个中继，断开之后重连都只能经 R2。
	if mc := connPtr(a, b.n.id.KeyID); mc != nil {
		_ = mc.conn.CloseWithError(0, "test")
	}
	eventually(t, "reconnected over R2", func() bool {
		return relayedVia(a, b.n.id.KeyID, r2.Addr) && relayedVia(b, a.n.id.KeyID, r2.Addr)
	})
	if s1 := relayStatus(a, r1); !strings.HasPrefix(s1, "连接失败：") && s1 != "连接已断开，正在重连" {
		t.Fatalf("R1 status = %q", s1)
	}
	if s2 := relayStatus(a, r2); s2 != "已连接" {
		t.Fatalf("R2 status = %q", s2)
	}
}

// 换令牌（指纹相同、令牌不同）：只重启这一个中继的连接，状态从"中继拒绝了登记"变回"已连接"，R2 不受影响。
func TestRelayTokenUpdateReconnects(t *testing.T) {
	r1, r2 := startTestRelay(t), startTestRelay(t)
	bad := r1
	bad.Token = make([]byte, relay.TokenSize)
	a := newDevice(t, "笔记本")
	if err := a.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	useRelay(t, a, bad)
	useRelay(t, a, r2)
	a.start(t)
	eventually(t, "R1 rejected, R2 connected", func() bool {
		return strings.HasPrefix(relayStatus(a, r1), "连接失败：中继拒绝了登记") && relayStatus(a, r2) == "已连接"
	})
	rc2 := relayConnOf(a, r2)
	if rc2 == nil {
		t.Fatal("no relayConn for R2")
	}
	if _, err := a.n.AddRelay(r1); err != nil {
		t.Fatal(err)
	}
	eventually(t, "R1 connected with the new token", func() bool {
		if s := relayStatus(a, r2); s != "已连接" {
			t.Fatalf("R2 status changed to %q", s)
		}
		return relayStatus(a, r1) == "已连接"
	})
	if relayConnOf(a, r2) != rc2 {
		t.Fatal("R2's connection must not be restarted")
	}
	if rs, _ := a.n.Relays(); len(rs) != 2 {
		t.Fatalf("relays = %v", rs)
	}
}

// A、B 只经 R1 连着时 A 删掉 R1：A 不再有 R1 的连接循环，也不再认为谁在中继上在线，连接断开后不再经中继拨号。
func TestRemovedRelayDropsItsOnlineState(t *testing.T) {
	r1 := startTestRelay(t)
	logs := &logRecorder{}
	a, b := newDevice(t, "笔记本"), newDevice(t, "台式机")
	a.n.opts.Logf = logs.Logf
	for _, d := range []*testDevice{a, b} {
		d.n.opts.RelayAfter = 200 * time.Millisecond
		d.n.candidatesHook = noDirect
	}
	a.start(t)
	joinCircle(t, a, b)
	clearAddrs(t, a, b)
	clearAddrs(t, b, a)
	useRelay(t, a, r1)
	useRelay(t, b, r1)
	b.start(t)
	eventually(t, "connected over R1", func() bool {
		return relayedVia(a, b.n.id.KeyID, r1.Addr) && onRelay(a, b.n.id.KeyID)
	})
	if err := a.n.RemoveRelay(r1.Fingerprint); err != nil {
		t.Fatal(err)
	}
	eventually(t, "R1 state dropped and the relayed connection gone", func() bool {
		return relayConnOf(a, r1) == nil && !onRelay(a, b.n.id.KeyID) && connPtr(a, b.n.id.KeyID) == nil
	})
	before := logs.count("（经中继）")
	time.Sleep(2 * time.Second) // 好几轮 relayAfter 和拨号
	if onRelay(a, b.n.id.KeyID) {
		t.Fatal("B must not be considered online at a relay")
	}
	if got := logs.count("（经中继）"); got != before || connPtr(a, b.n.id.KeyID) != nil {
		t.Fatalf("dialed over the removed relay: %d relayed connects (before %d)", got, before)
	}
}

// UDP 被拦（A 的 UDP 拨号一直没有回应）：A 经 TCP 连上中继，状态是"已连接（TCP）"，A、B 经中继连上并能发文件。
func TestRelayFallsBackToTCP(t *testing.T) {
	cfg, _ := startTCPRelay(t)
	a, b := newDevice(t, "笔记本"), newDevice(t, "台式机")
	for _, d := range []*testDevice{a, b} {
		d.n.opts.RelayAfter = 200 * time.Millisecond
		d.n.candidatesHook = noDirect
	}
	blockRelayUDP(a, newUDPGate())
	a.start(t)
	joinCircle(t, a, b)
	clearAddrs(t, a, b)
	clearAddrs(t, b, a)
	useRelay(t, a, cfg)
	useRelay(t, b, cfg)
	b.start(t)
	eventually(t, "A over TCP, B over UDP", func() bool {
		return relayStatus(a, cfg) == "已连接（TCP）" && relayStatus(b, cfg) == "已连接"
	})
	if cl := relayClientOf(a, cfg); cl == nil || cl.Transport() != "tcp" || cl.ObservedAddr() != "" {
		t.Fatal("A must be registered over TCP without an observed address")
	}
	eventually(t, "connected over the relay", func() bool {
		return relayedVia(a, b.n.id.KeyID, cfg.Addr) && relayedVia(b, a.n.id.KeyID, cfg.Addr)
	})
	id, err := a.n.QueueSend(b.n.id.KeyID, plan(t, "经 TCP 中继", map[string]string{"tcp.txt": "经 TCP 的内容"}))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, a, id, store.SendDone)
	if got := received(t, b, "tcp.txt"); got != "经 TCP 的内容" {
		t.Fatalf("received %q", got)
	}
}

// 先拦 UDP、经 TCP 连上；放开 UDP 后，探测发现 UDP 恢复，改回 UDP，状态变回"已连接"，A、B 仍然在线。
func TestRelaySwitchesBackToUDP(t *testing.T) {
	cfg, _ := startTCPRelay(t)
	logs := &logRecorder{}
	a, b := newDevice(t, "笔记本"), newDevice(t, "台式机")
	a.n.opts.Logf = logs.Logf
	a.n.opts.RelayUDPProbe = 200 * time.Millisecond
	for _, d := range []*testDevice{a, b} {
		d.n.opts.RelayAfter = 200 * time.Millisecond
		d.n.candidatesHook = noDirect
	}
	gate := newUDPGate()
	blockRelayUDP(a, gate)
	a.start(t)
	joinCircle(t, a, b)
	clearAddrs(t, a, b)
	clearAddrs(t, b, a)
	useRelay(t, a, cfg)
	useRelay(t, b, cfg)
	b.start(t)
	eventually(t, "A over TCP", func() bool { return relayStatus(a, cfg) == "已连接（TCP）" })
	eventually(t, "A and B online", func() bool {
		return online(t, a, b.n.id.KeyID) && online(t, b, a.n.id.KeyID)
	})
	if logs.contains("UDP 恢复了") {
		t.Fatal("must not switch back while UDP is blocked")
	}
	gate.open()
	eventually(t, "back on UDP", func() bool {
		cl := relayClientOf(a, cfg)
		return relayStatus(a, cfg) == "已连接" && cl != nil && cl.Transport() == "udp"
	})
	if !logs.contains("中继 " + cfg.Addr + " 的 UDP 恢复了，改回 UDP") {
		t.Fatal("switch-back log missing")
	}
	eventually(t, "A and B online again over the relay", func() bool {
		return relayedVia(a, b.n.id.KeyID, cfg.Addr) && relayedVia(b, a.n.id.KeyID, cfg.Addr) &&
			online(t, a, b.n.id.KeyID) && online(t, b, a.n.id.KeyID)
	})
}

// 探测 UDP 成功、但改用 UDP 重新连时 UDP 握手失败（Review Focus 4）：回到 TCP，并等满一个间隔才再探测，不反复切换。
func TestUDPProbeNoFlap(t *testing.T) {
	cfg, _ := startTCPRelay(t)
	logs := &logRecorder{}
	a := newDevice(t, "笔记本")
	if err := a.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	const probe = 300 * time.Millisecond
	a.n.opts.Logf = logs.Logf
	a.n.opts.RelayUDPProbe = probe
	a.n.relayTCPDelay = 300 * time.Millisecond
	a.n.relayUDPDial = func(relay.DialFunc) relay.DialFunc {
		return func(context.Context, string, *tls.Config) (*quic.Conn, error) {
			return nil, errors.New("udp blocked")
		}
	}
	a.n.relayProbeDial = func(real relay.DialFunc) relay.DialFunc { return real } // 探测总是成功
	useRelay(t, a, cfg)
	a.start(t)
	eventually(t, "A over TCP", func() bool { return relayStatus(a, cfg) == "已连接（TCP）" })
	before := logs.count("UDP 恢复了，改回 UDP")
	time.Sleep(3 * probe)
	if n := logs.count("UDP 恢复了，改回 UDP") - before; n > 3 {
		t.Fatalf("switched back %d times in 3 probe intervals", n)
	}
	if logs.count("UDP 恢复了，改回 UDP") == 0 {
		time.Sleep(probe) // 至少要探测过一次，否则这个测试什么也没测到
		if logs.count("UDP 恢复了，改回 UDP") == 0 {
			t.Fatal("probe never ran")
		}
	}
	eventually(t, "still on TCP", func() bool {
		cl := relayClientOf(a, cfg)
		return relayStatus(a, cfg) == "已连接（TCP）" && cl != nil && cl.Transport() == "tcp"
	})
}

// 经 TCP 连着时中继那边的 TCP 连接被掐断（Review Focus 3）：A 重新经 TCP 连上，A、B 恢复在线。
func TestRelayTCPReconnectsAfterDrop(t *testing.T) {
	cfg, srv := startTCPRelay(t)
	a, b := newDevice(t, "笔记本"), newDevice(t, "台式机")
	for _, d := range []*testDevice{a, b} {
		d.n.opts.RelayAfter = 200 * time.Millisecond
		d.n.candidatesHook = noDirect
	}
	blockRelayUDP(a, newUDPGate())
	a.start(t)
	joinCircle(t, a, b)
	clearAddrs(t, a, b)
	clearAddrs(t, b, a)
	useRelay(t, a, cfg)
	useRelay(t, b, cfg)
	b.start(t)
	eventually(t, "A over TCP and connected to B over the relay", func() bool {
		return relayStatus(a, cfg) == "已连接（TCP）" &&
			relayedVia(a, b.n.id.KeyID, cfg.Addr) && relayedVia(b, a.n.id.KeyID, cfg.Addr)
	})
	old := relayClientOf(a, cfg)
	srv.CloseTCPConns()
	eventually(t, "A re-registered over TCP", func() bool {
		cl := relayClientOf(a, cfg)
		return cl != nil && cl != old && cl.Transport() == "tcp" && relayStatus(a, cfg) == "已连接（TCP）"
	})
	eventually(t, "A and B back online over the relay", func() bool {
		return relayedVia(a, b.n.id.KeyID, cfg.Addr) && relayedVia(b, a.n.id.KeyID, cfg.Addr) &&
			online(t, a, b.n.id.KeyID) && online(t, b, a.n.id.KeyID)
	})
}

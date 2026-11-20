package node

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"portide/core/relay"
	"portide/core/relayserver"
	"portide/core/store"
)

func TestValidCandidates(t *testing.T) {
	in := []string{
		"1.2.3.4:51000", "1.2.3.4:51000", // 重复
		"[2001:db8::1]:7443",
		"relay.example.com:7443",         // 域名：不解析
		"0.0.0.0:7443", "224.0.0.1:7443", // 未指定、组播
		"1.2.3.4:0", "1.2.3.4:70000", "1.2.3.4", "garbage",
	}
	want := []string{"1.2.3.4:51000", "[2001:db8::1]:7443"}
	if got := validCandidates(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("validCandidates = %q, want %q", got, want)
	}
	var many []string
	for i := 1; i <= 20; i++ {
		many = append(many, fmt.Sprintf("10.0.0.%d:7443", i))
	}
	if got := validCandidates(many); len(got) > maxCandidates {
		t.Fatalf("at most %d candidates, got %d", maxCandidates, len(got))
	}
}

// 本机网卡地址的顺序：私有 IPv4（真正的局域网地址）在前，然后是全局单播 IPv6，其余按原顺序在后，
// 截断到 maxCandidates 时不会把它们挤掉。
func TestOrderLocalIPs(t *testing.T) {
	in := []string{"2001:db8::1", "100.64.0.1", "192.168.1.5", "fd00::5", "2001:db8::2", "10.0.0.2", "203.0.113.7"}
	want := []string{"192.168.1.5", "10.0.0.2", "2001:db8::1", "2001:db8::2", "100.64.0.1", "fd00::5", "203.0.113.7"}
	if got := orderLocalIPs(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("orderLocalIPs = %q, want %q", got, want)
	}
}

// 公网 IPv6 排在"其他 IPv4"（Docker 网桥等也是私有地址，公网 IPv4 更不用说）之前，不会被挤掉。
func TestCandidatesPreferIPv6OverOtherIPv4(t *testing.T) {
	got := orderLocalIPs([]string{"203.0.113.5", "2001:db8::1", "10.0.0.2", "172.17.0.1"})
	want := []string{"10.0.0.2", "172.17.0.1", "2001:db8::1", "203.0.113.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orderLocalIPs = %q, want %q", got, want)
	}
}

// relayedPair 让两台新设备只能经中继连上，两边都用候选地址钩子 hook；返回时已经连上。
func relayedPair(t *testing.T, cfg relay.Config, hook func([]string) []string) (laptop, desktop *testDevice) {
	t.Helper()
	return startRelayedPair(t, cfg, newDevice(t, "笔记本"), newDevice(t, "台式机"), hook)
}

// 先经中继连上，交换候选地址之后自动升级为直连（本机回环上没有 NAT，升级必然成功）。
func TestRelayedConnectionUpgradesToDirect(t *testing.T) {
	logs, dlogs := &logRecorder{}, &logRecorder{}
	cfg := startTestRelay(t)
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	laptop.n.opts.Logf, desktop.n.opts.Logf = logs.Logf, dlogs.Logf
	laptop.n.opts.RelayAfter = 200 * time.Millisecond
	desktop.n.opts.RelayAfter = 200 * time.Millisecond
	laptop.start(t)
	joinCircle(t, laptop, desktop)
	if err := desktop.n.Store().UpsertPeer(store.Peer{KeyID: laptop.n.id.KeyID}); err != nil {
		t.Fatal(err)
	}
	useRelay(t, laptop, cfg)
	useRelay(t, desktop, cfg)
	desktop.start(t)
	eventually(t, "upgraded to a direct connection", func() bool {
		mc := connPtr(laptop, desktop.n.id.KeyID)
		mc2 := connPtr(desktop, laptop.n.id.KeyID)
		return mc != nil && mc2 != nil && !relay.IsRelayed(mc.conn.RemoteAddr()) && !relay.IsRelayed(mc2.conn.RemoteAddr())
	})
	// 至少一边记下"已切换到直连"（另一边可能在对方关掉中继那条之后才登记直连，看到的就是普通的"已连接"）。
	eventually(t, "the upgrade is logged", func() bool {
		return logs.contains("已切换到直连 台式机") || dlogs.contains("已切换到直连 笔记本")
	})
	for _, d := range mustDevices(t, laptop) {
		if d.KeyID == desktop.n.id.KeyID && (!d.Online || isRelayAddr(d.Addr)) {
			t.Fatalf("after the upgrade the address must be the direct one: %+v", d)
		}
	}
	// 升级之后照常收发。
	id, err := laptop.n.QueueSend(desktop.n.id.KeyID, plan(t, "直连之后", nil))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendDone)
}

// 候选地址都连不上（例如对称 NAT）：留在中继上，照常收发。
func TestUnreachableCandidatesStayRelayed(t *testing.T) {
	unreachable := func([]string) []string { return []string{unreachableAddr} }
	laptop, desktop := relayedPair(t, startTestRelay(t), unreachable)
	time.Sleep(2 * dialInterval)
	if mc := connPtr(laptop, desktop.n.id.KeyID); mc == nil || !relay.IsRelayed(mc.conn.RemoteAddr()) {
		t.Fatal("the connection must stay on the relay when no candidate is reachable")
	}
	id, err := laptop.n.QueueSend(desktop.n.id.KeyID, plan(t, "还在中继上", nil))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendDone)
}

var errPunchBlocked = errors.New("punch blocked by test")

// 一轮打洞（为一条候选地址消息发起的那组直连）还没结束时又收到候选地址：不另起一轮；这一轮结束之后才能开始下一轮。
func TestPunchRoundsDoNotOverlap(t *testing.T) {
	var dials atomic.Int32
	gate := make(chan struct{})
	one := func([]string) []string { return []string{unreachableAddr} }
	cfg := startTestRelay(t)
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	laptop.n.upgradeEvery, desktop.n.upgradeEvery = 100*time.Millisecond, 100*time.Millisecond
	laptop.n.punchHook = func(string) error { dials.Add(1); <-gate; return errPunchBlocked } // 这一轮的直连一直卡在拨号阶段
	desktop.n.punchHook = func(string) error { return errPunchBlocked }
	laptop, desktop = startRelayedPair(t, cfg, laptop, desktop, one)
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release) // 先于守护进程退出执行，不然卡住的直连会让它等不完
	eventually(t, "the first punch round started", func() bool { return dials.Load() >= 1 })
	time.Sleep(time.Second) // 期间对方又发来大约 10 次候选地址
	if got := dials.Load(); got != 1 {
		t.Fatalf("%d dial attempts while one round was in flight, want 1", got)
	}
	release()
	eventually(t, "a new round after the first one finished", func() bool { return dials.Load() > 1 })
}

// noDirect 是候选地址钩子：不发任何候选地址，连接留在中继上。
func noDirect([]string) []string { return nil }

func isRelayAddr(addr string) bool {
	_, ok := relay.RelayedVia(addr)
	return ok
}

func mustDevices(t *testing.T, d *testDevice) []DeviceStatus {
	t.Helper()
	devs, err := d.n.Devices()
	if err != nil {
		t.Fatal(err)
	}
	return devs
}

// unreachableAddr 是模拟打不通时给出的候选地址。
const unreachableAddr = "127.0.0.1:1"

// gatedCandidates 返回一个候选地址钩子：open 为 false 时只给不可达的地址（模拟当前网络打不通），为 true 时照常。
func gatedCandidates(open *atomic.Bool) func([]string) []string {
	return func(in []string) []string {
		if open.Load() {
			return in
		}
		return []string{unreachableAddr}
	}
}

// failUnreachable 是直连钩子：向 unreachableAddr 的直连立刻失败。真的去拨要等满 punchTimeout，
// 这一轮没结束之前新的候选地址都会被丢弃，网络变好之后要十几秒才会升级。
func failUnreachable(addr string) error {
	if addr == unreachableAddr {
		return errPunchBlocked
	}
	return nil
}

// 一开始打不通、留在中继上；网络变好之后（例如回到同一个局域网），定期重试会自动升级为直连。
func TestUpgradeRetriesUntilReachable(t *testing.T) {
	var open atomic.Bool
	hook := gatedCandidates(&open)
	cfg := startTestRelay(t)
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	laptop.n.upgradeEvery, desktop.n.upgradeEvery = 300*time.Millisecond, 300*time.Millisecond
	laptop.n.punchHook, desktop.n.punchHook = failUnreachable, failUnreachable
	laptop, desktop = startRelayedPair(t, cfg, laptop, desktop, hook)
	time.Sleep(time.Second)
	if mc := connPtr(laptop, desktop.n.id.KeyID); mc == nil || !mc.relayed {
		t.Fatal("must stay relayed while no candidate is reachable")
	}
	open.Store(true)
	eventually(t, "upgraded after the network became reachable", func() bool {
		mc := connPtr(laptop, desktop.n.id.KeyID)
		return mc != nil && !mc.relayed
	})
}

// 经中继传到一半时升级为直连：经中继的那条被关掉，传输在直连上续传完成，内容一致。
func TestTransferSurvivesUpgrade(t *testing.T) {
	var open atomic.Bool
	hook := gatedCandidates(&open)
	cfg := startTestRelayWith(t, relayserver.Limits{SessionRate: 512 << 10}) // 经中继限速 512 KiB/s
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	dlogs := &logRecorder{}
	desktop.n.opts.Logf = dlogs.Logf
	laptop.n.upgradeEvery, desktop.n.upgradeEvery = 300*time.Millisecond, 300*time.Millisecond
	laptop.n.punchHook, desktop.n.punchHook = failUnreachable, failUnreachable
	laptop, desktop = startRelayedPair(t, cfg, laptop, desktop, hook)

	content := bytes.Repeat([]byte("直连续传"), (4<<20)/len("直连续传")) // 约 4 MiB：经中继要好几秒
	id, err := laptop.n.QueueSend(desktop.n.id.KeyID, plan(t, "", map[string]string{"大文件.bin": string(content)}))
	if err != nil {
		t.Fatal(err)
	}
	// 等接收方的临时文件里已经落了一部分数据：续传时对方从这个长度（偏移 > 0）接着发，而不是从头再来。
	dir, _ := desktop.n.SaveDir()
	eventually(t, "part of the file received over the relay", func() bool {
		parts, _ := filepath.Glob(filepath.Join(dir, ".portide-parts", "*.part"))
		for _, p := range parts {
			if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
				return true
			}
		}
		return false
	})
	if mc := connPtr(laptop, desktop.n.id.KeyID); mc == nil || !mc.relayed {
		t.Fatal("the transfer must start on the relay")
	}
	open.Store(true)
	waitStatus(t, laptop, id, store.SendDone)
	if got := received(t, desktop, "大文件.bin"); got != string(content) {
		t.Fatalf("received %d bytes, want %d intact", len(got), len(content))
	}
	if mc := connPtr(laptop, desktop.n.id.KeyID); mc == nil || mc.relayed {
		t.Fatal("the transfer must finish on the direct connection")
	}
	// 直连上是续传（接收方按已有的临时文件给出偏移），不是重新开始一次新的接收。
	if !dlogs.contains("继续接收「笔记本」") || dlogs.count("开始接收「笔记本」") != 1 {
		t.Fatal("the direct leg must resume the relayed transfer, not start a new receive")
	}
}

// startRelayedPair 让已经建好的两台设备只能经中继连上（局域网里互相找不到），返回时已经连上。
func startRelayedPair(t *testing.T, cfg relay.Config, laptop, desktop *testDevice, hook func([]string) []string) (*testDevice, *testDevice) {
	t.Helper()
	laptop.n.opts.RelayAfter = 200 * time.Millisecond
	desktop.n.opts.RelayAfter = 200 * time.Millisecond
	laptop.n.candidatesHook, desktop.n.candidatesHook = hook, hook
	laptop.start(t)
	joinCircle(t, laptop, desktop)
	if err := desktop.n.Store().UpsertPeer(store.Peer{KeyID: laptop.n.id.KeyID}); err != nil {
		t.Fatal(err)
	}
	useRelay(t, laptop, cfg)
	useRelay(t, desktop, cfg)
	desktop.start(t)
	eventually(t, "connected", func() bool { return connPtr(laptop, desktop.n.id.KeyID) != nil })
	return laptop, desktop
}

// setCandidates 模拟 mDNS 在局域网里发现了 keyID 的地址。
func setCandidates(d *testDevice, keyID string, addrs ...string) {
	d.n.daemon.mu.Lock()
	d.n.daemon.candidates[keyID] = addrs
	d.n.daemon.mu.Unlock()
}

// 经中继连着、候选地址交换也没给出能用的地址，但 mDNS 在局域网里找到了对方：不等下一轮交换，直接升级为直连。
func TestLANDiscoveryUpgradesRelayed(t *testing.T) {
	laptop, desktop := relayedPair(t, startTestRelay(t), noDirect)
	if mc := connPtr(laptop, desktop.n.id.KeyID); mc == nil || !mc.relayed {
		t.Fatal("must start relayed")
	}
	setCandidates(laptop, desktop.n.id.KeyID, desktop.n.Addr())
	eventually(t, "upgraded after mDNS found the peer", func() bool {
		mc := connPtr(laptop, desktop.n.id.KeyID)
		return mc != nil && !mc.relayed
	})
}

// 对方在中继上在线已经超过 relayAfter，但 mDNS 刚在局域网里找到了它：重连时先试局域网，不先走中继。
func TestReconnectTriesLANBeforeRelay(t *testing.T) {
	laptop, desktop := relayedPair(t, startTestRelay(t), noDirect)
	lk, dk := laptop.n.id.KeyID, desktop.n.id.KeyID
	// 只看重连时的拨号顺序：关掉"经中继连着时按 mDNS 地址升级"，台式机也不主动重连（看不到笔记本在中继上在线）。
	for _, d := range []*testDevice{laptop, desktop} {
		d.n.daemon.mu.Lock()
		d.n.daemon.lanTried[lk] = time.Now().Add(time.Hour)
		d.n.daemon.lanTried[dk] = time.Now().Add(time.Hour)
		for _, rc := range d.n.daemon.relays {
			if d == desktop {
				delete(rc.online, lk)
			}
		}
		d.n.daemon.mu.Unlock()
	}
	setCandidates(laptop, dk, desktop.n.Addr())
	mc := connPtr(laptop, dk)
	_ = mc.conn.CloseWithError(0, "test: drop the relayed connection")
	eventually(t, "reconnected directly", func() bool {
		mc := connPtr(laptop, dk)
		return mc != nil && !mc.relayed
	})
}

// 一轮直连都没连上时记一条日志，写明每个地址失败的原因，排查"为什么一直走中继"时用得上。
func TestFailedDirectRoundIsLogged(t *testing.T) {
	logs := &logRecorder{}
	unreachable := func([]string) []string { return []string{unreachableAddr} }
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	desktop.n.opts.Logf = logs.Logf
	laptop.n.punchHook, desktop.n.punchHook = failUnreachable, failUnreachable
	startRelayedPair(t, startTestRelay(t), laptop, desktop, unreachable)
	eventually(t, "the failed round is logged", func() bool {
		return logs.contains("没能直连 笔记本（候选地址交换），继续经中继：" + unreachableAddr)
	})
}

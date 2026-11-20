package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"portide/core/keystore"
	"portide/core/pairing"
	"portide/core/store"
)

func connPtr(d *testDevice, keyID string) *memberConn {
	d.n.daemon.mu.Lock()
	defer d.n.daemon.mu.Unlock()
	return d.n.daemon.conns[keyID]
}

type testDevice struct {
	n    *Node
	keys keystore.Memory
	dir  string
}

// newDevice 初始化并打开一台测试设备（关闭 mDNS，只监听回环地址）。
func newDevice(t testing.TB, name string) *testDevice {
	t.Helper()
	dir := t.TempDir()
	keys := keystore.Memory{}
	if _, err := Init(dir, keys, name); err != nil {
		t.Fatal(err)
	}
	n, err := Open(Options{
		DataDir: dir, Keys: keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true,
		PollInterval: 50 * time.Millisecond, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	if err := n.SetSaveDir(filepath.Join(dir, "received")); err != nil {
		t.Fatal(err)
	}
	return &testDevice{n: n, keys: keys, dir: dir}
}

// start 在后台运行守护进程，测试结束时停止。
func (d *testDevice) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.n.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-d.n.Ready()
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func online(t *testing.T, d *testDevice, keyID string) bool {
	devs, err := d.n.Devices()
	if err != nil {
		return false
	}
	for _, dev := range devs {
		if dev.KeyID == keyID {
			return dev.Online
		}
	}
	return false
}

// invite 模拟 `portide pair`：创建设备圈（如需要）并写入配对会话，等守护进程开放配对。
func invite(t *testing.T, d *testDevice) (code string, sessionID int64) {
	t.Helper()
	if err := d.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	code, _ = pairing.NewCode()
	id, err := d.n.Store().CreatePairSession(code, pairing.CodeTTL)
	if err != nil {
		t.Fatal(err)
	}
	// 等守护进程的下一次轮询开放配对：开放之前传输层不接受配对连接（机器很忙时一次轮询可能要好几百毫秒）。
	eventually(t, "pairing open", func() bool {
		d.n.mu.Lock()
		dm := d.n.daemon
		d.n.mu.Unlock()
		if dm == nil {
			return false
		}
		dm.mu.Lock()
		defer dm.mu.Unlock()
		return dm.pair != nil && dm.pair.ID == id
	})
	return code, id
}

func TestPairThenBothOnline(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	desktop := newDevice(t, "台式机")
	laptop.start(t)

	code, sid := invite(t, laptop)
	res, err := desktop.n.Join(context.Background(), code, []string{laptop.n.Addr()}, false)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if len(res.Members) != 2 || res.CircleID != laptop.n.CircleID() {
		t.Fatalf("join result = %+v", res)
	}
	// 邀请方在加入方收到欢迎消息、关闭配对流之后才写入会话结果，所以要等一下。
	eventually(t, "inviter records the finished session", func() bool {
		ps, _ := laptop.n.Store().GetPairSession(sid)
		return ps.Status == store.PairDone && ps.JoinerName == "台式机"
	})

	// 台式机启动守护进程后，应通过 Join 记下的地址连上笔记本，双方都看到对方在线。
	desktop.start(t)
	eventually(t, "laptop sees desktop online", func() bool { return online(t, laptop, desktop.n.Identity().KeyID) })
	eventually(t, "desktop sees laptop online", func() bool { return online(t, desktop, laptop.n.Identity().KeyID) })
	if !laptop.n.DaemonRunning() {
		t.Fatal("heartbeat should mark the daemon as running")
	}
}

func TestWrongCodeThreeTimesClosesSession(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	laptop.start(t)
	_, sid := invite(t, laptop)

	for i := 0; i < pairing.MaxAttempts; i++ {
		intruder := newDevice(t, "陌生人")
		_, err := intruder.n.Join(context.Background(), "00000000", []string{laptop.n.Addr()}, false)
		if !errors.Is(err, pairing.ErrWrongCode) {
			t.Fatalf("attempt %d: err = %v, want ErrWrongCode", i+1, err)
		}
	}
	eventually(t, "session closed after max attempts", func() bool {
		ps, _ := laptop.n.Store().GetPairSession(sid)
		return ps.Status == store.PairFailed && ps.Error == "too many attempts"
	})
	devs, _ := laptop.n.Devices()
	if len(devs) != 1 {
		t.Fatalf("no one should have joined, devices = %+v", devs)
	}
}

func TestThirdDeviceLearnedThroughChainSync(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	desktop := newDevice(t, "台式机")
	tablet := newDevice(t, "平板")
	laptop.start(t)

	code, _ := invite(t, laptop)
	if _, err := desktop.n.Join(context.Background(), code, []string{laptop.n.Addr()}, false); err != nil {
		t.Fatal(err)
	}
	desktop.start(t)
	eventually(t, "desktop online", func() bool { return online(t, laptop, desktop.n.Identity().KeyID) })

	// 平板由笔记本邀请；台式机没参与，但应通过与笔记本的签名链同步认识平板。
	code, _ = invite(t, laptop)
	if _, err := tablet.n.Join(context.Background(), code, []string{laptop.n.Addr()}, false); err != nil {
		t.Fatal(err)
	}
	// 笔记本在配对成功后把新链推送给已连接的台式机。
	eventually(t, "desktop learns about tablet via push", func() bool {
		devs, _ := desktop.n.Devices()
		return len(devs) == 3
	})
	// 测试里没有 mDNS，平板通过静态地址找到台式机；真实网络中由 mDNS 发现。
	tablet.n.opts.StaticPeers = []string{desktop.n.Addr()}
	tablet.start(t)
	eventually(t, "desktop sees tablet online", func() bool { return online(t, desktop, tablet.n.Identity().KeyID) })
	devs, _ := desktop.n.Devices()
	if len(devs) != 3 {
		t.Fatalf("desktop should know 3 members, got %d", len(devs))
	}
}

// 监听失败时 Run 必须返回错误，并且不能让等待 Ready 的调用永远阻塞。
func TestRunReportsListenFailure(t *testing.T) {
	busy, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	d := newDevice(t, "笔记本")
	d.n.opts.ListenAddr = busy.LocalAddr().String()
	if err := d.n.Run(context.Background()); err == nil {
		t.Fatal("Run must fail when the port is taken")
	}
	select {
	case <-d.n.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("Ready must be closed after a listen failure")
	}
	if addr := d.n.Addr(); addr != "" {
		t.Fatalf("Addr after a listen failure = %q, want empty", addr)
	}
	if err := d.n.Run(context.Background()); err == nil {
		t.Fatal("a second Run on the same node must fail")
	}
}

func connCount(d *testDevice) int {
	d.n.daemon.mu.Lock()
	defer d.n.daemon.mu.Unlock()
	return len(d.n.daemon.conns)
}

// 本端发起的重新拨号不能替换掉一条健康的旧连接：对端早就登记了旧连接，不会因为新连接
// 完成了 Hello 就把旧的换掉，所以新连接应该在交换 Hello 之前就被本端自己关闭。
func TestRedialDoesNotReplaceHealthyLink(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	desktop := newDevice(t, "台式机")
	tablet := newDevice(t, "平板")
	laptop.start(t)

	code, _ := invite(t, laptop)
	if _, err := desktop.n.Join(context.Background(), code, []string{laptop.n.Addr()}, false); err != nil {
		t.Fatal(err)
	}
	code, _ = invite(t, laptop)
	// 平板加入但从不启动守护进程：设备圈有 3 个成员、1 个始终离线，
	// 这样 dialMembers 才会尝试静态地址（它只在少于 members-1 条连接时才拨静态地址）。
	if _, err := tablet.n.Join(context.Background(), code, []string{laptop.n.Addr()}, false); err != nil {
		t.Fatal(err)
	}

	_, laptopPort, _ := net.SplitHostPort(laptop.n.Addr())
	desktop.n.opts.StaticPeers = []string{"localhost:" + laptopPort} // 和 Join 记下的地址写法不同
	desktop.start(t)

	desktopID := desktop.n.Identity().KeyID
	eventually(t, "both online", func() bool {
		return online(t, laptop, desktopID) && online(t, desktop, laptop.n.Identity().KeyID)
	})
	first := connPtr(laptop, desktopID)
	if first == nil {
		t.Fatal("laptop has no connection to desktop")
	}

	for i := 0; i < 5; i++ {
		desktop.n.dialMembers(context.Background())
		time.Sleep(300 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	again := connPtr(laptop, desktopID)
	if again != first {
		t.Fatal("redial replaced a healthy link")
	}
	if again.conn.Context().Err() != nil {
		t.Fatal("the surviving connection was closed")
	}
	if !online(t, laptop, desktopID) || !online(t, desktop, laptop.n.Identity().KeyID) {
		t.Fatal("both devices must still see each other online")
	}
}

// 同一方通过多个地址并发拨号产生重复连接时，两端必须保留同一条，不能把两条都关掉。
func TestDuplicateDialsFromOneSideKeepOneLink(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	desktop := newDevice(t, "台式机")
	laptop.start(t)
	code, _ := invite(t, laptop)
	if _, err := desktop.n.Join(context.Background(), code, []string{laptop.n.Addr()}, false); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(laptop.n.Addr())
	// 同一个笔记本的三个地址写法：台式机会同时从多个地址拨过去。
	desktop.n.opts.StaticPeers = []string{"127.0.0.1:" + port, "localhost:" + port, "[::ffff:127.0.0.1]:" + port}
	desktop.start(t)

	eventually(t, "both online", func() bool {
		return online(t, laptop, desktop.n.Identity().KeyID) && online(t, desktop, laptop.n.Identity().KeyID)
	})
	time.Sleep(1500 * time.Millisecond) // 让所有并发拨号都完成并去重
	if !online(t, laptop, desktop.n.Identity().KeyID) || !online(t, desktop, laptop.n.Identity().KeyID) {
		t.Fatal("deduplication must not tear down every link")
	}
	if connCount(laptop) != 1 || connCount(desktop) != 1 {
		t.Fatalf("conns: laptop=%d desktop=%d, want 1 each", connCount(laptop), connCount(desktop))
	}
}

// logRecorder 收集日志行，供测试断言。
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

// count 返回包含 s 的日志行数。
func (r *logRecorder) count(s string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.lines {
		if strings.Contains(l, s) {
			n++
		}
	}
	return n
}

func (r *logRecorder) contains(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// 另一个设备圈的设备主动连过来：在圈子证明那一步就被拒绝，双方都不登记这条连接。
func TestDeviceFromAnotherCircleIsRejected(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	if err := laptop.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	logs := &logRecorder{}
	laptop.n.opts.Logf = logs.Logf
	laptop.start(t)

	stranger := newDevice(t, "陌生人")
	if err := stranger.n.EnsureCircle(); err != nil { // 它有自己的设备圈和发现密钥
		t.Fatal(err)
	}
	stranger.start(t)
	stranger.n.dialOne(context.Background(), laptop.n.Addr(), false, dialTimeout)

	eventually(t, "laptop logs the rejection", func() bool { return logs.contains("不属于本设备圈") })
	if connCount(laptop) != 0 || connCount(stranger) != 0 {
		t.Fatalf("no link may be registered: laptop=%d stranger=%d", connCount(laptop), connCount(stranger))
	}
	if devs, _ := laptop.n.Devices(); len(devs) != 1 {
		t.Fatalf("the stranger must not appear in the laptop's devices: %+v", devs)
	}
}

// 对端发布的地址里可能有到不了的（例如虚拟网卡）：join 不能在一个地址上卡到握手超时，要尽快换下一个。
func TestJoinSkipsUnreachableAddressQuickly(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	desktop := newDevice(t, "台式机")
	laptop.start(t)
	code, _ := invite(t, laptop)
	start := time.Now()
	// 192.0.2.1 属于文档专用的 TEST-NET-1，发过去的包不会有回应。
	if _, err := desktop.n.Join(context.Background(), code, []string{"192.0.2.1:7443", laptop.n.Addr()}, false); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if el := time.Since(start); el > 15*time.Second {
		t.Fatalf("join took %s; an unreachable address must not stall it", el.Round(time.Second))
	}
}

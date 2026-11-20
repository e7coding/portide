package app

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"portide/core/keystore"
	"portide/core/node"
	"portide/core/pairing"
)

// 测试一律用文件密钥库、不设置 PORTIDE_PASSPHRASE，这样能走到"输入口令"这一步，也不会碰系统密钥库。
func useFileKeystore(t *testing.T) {
	t.Setenv("PORTIDE_KEYSTORE", "file")
	t.Setenv("PORTIDE_PASSPHRASE", "")
}

func newService(t *testing.T, dir string) *Service {
	t.Helper()
	s := New(Config{DataDir: dir, ListenAddr: "127.0.0.1:0", DisableMDNS: true, PollInterval: 50 * time.Millisecond, Logf: t.Logf})
	t.Cleanup(func() { Stop(s) })
	return s
}

func wantPhase(t *testing.T, s *Service, phase string) State {
	t.Helper()
	st := s.State()
	if st.Phase != phase {
		t.Fatalf("phase = %q (error %q), want %q", st.Phase, st.Error, phase)
	}
	return st
}

// 首次启动：设置口令 → 填设备名 → 自动新建只有本机的设备圈 → 核心开始运行。
func TestFirstRunCreatesCircle(t *testing.T) {
	useFileKeystore(t)
	s := newService(t, t.TempDir())
	if st := wantPhase(t, s, PhaseLocked); !st.FirstPassphrase {
		t.Fatal("a fresh data dir must ask to set a passphrase")
	}
	if err := s.Unlock("pw"); err != nil {
		t.Fatal(err)
	}
	if st := wantPhase(t, s, PhaseSetup); !st.NeedsName || st.DefaultName == "" || len(st.NameSuggestions) < 2 {
		t.Fatalf("setup state = %+v", st)
	}
	if err := s.Init(""); err == nil || err.Error() != "请填写设备名" {
		t.Fatalf("empty name: err = %v", err)
	}
	if err := s.Init("台式机"); err != nil {
		t.Fatal(err)
	}
	// Init 之后自动建好单机圈并启动核心。
	if st := wantPhase(t, s, PhaseRunning); st.Name != "台式机" || st.KeyID == "" || len(st.NameSuggestions) != 0 {
		t.Fatalf("after init: %+v", st)
	}
	n, err := s.node()
	if err != nil || !n.InCircle() || !n.DaemonRunning() {
		t.Fatalf("node = %v, err = %v", n, err)
	}
	if names := devicesOf(t, s); len(names) != 1 || names[0] != "台式机" {
		t.Fatalf("devices = %v, want only this device", names)
	}
}

// New 只存配置，不启动流程：桌面应用的第二个实例在 Wails 的单实例检查里就退出了，不会调用任何方法，
// 也就不会碰密钥库和数据库——这里用一个无效的 PORTIDE_KEYSTORE 验证 New 本身不会报错、不会在数据目录里
// 留下任何东西；第一次真正调用方法（State）时才会跑 boot，并且失败，报告 PhaseError。
func TestNewDoesNotBootEagerly(t *testing.T) {
	t.Setenv("PORTIDE_KEYSTORE", "bogus")
	dir := t.TempDir()
	s := New(Config{DataDir: dir, ListenAddr: "127.0.0.1:0", DisableMDNS: true, Logf: t.Logf})
	t.Cleanup(func() { Stop(s) })

	if _, err := os.Stat(filepath.Join(dir, "portide.db")); !os.IsNotExist(err) {
		t.Fatalf("New must not touch the data dir before boot; portide.db stat err = %v", err)
	}

	wantPhase(t, s, PhaseError)
}

// 第二次启动：口令不对时停在输入口令那一步并提示，口令对了就直接运行。
func TestRestartAsksForPassphrase(t *testing.T) {
	useFileKeystore(t)
	dir := t.TempDir()
	first := New(Config{DataDir: dir, ListenAddr: "127.0.0.1:0", DisableMDNS: true, Logf: t.Logf})
	t.Cleanup(func() { Stop(first) }) // Stop 可以重复调用，这里只是保证测试失败时也不会漏关
	if err := first.Unlock("pw"); err != nil {
		t.Fatal(err)
	}
	if err := first.Init("台式机"); err != nil {
		t.Fatal(err)
	}
	Stop(first)

	s := newService(t, dir)
	if st := wantPhase(t, s, PhaseLocked); st.FirstPassphrase {
		t.Fatal("the keystore exists: must ask for the passphrase, not to set one")
	}
	if err := s.Unlock("bad"); err == nil || err.Error() != "口令不对" {
		t.Fatalf("wrong passphrase: err = %v", err)
	}
	wantPhase(t, s, PhaseLocked)
	if err := s.Unlock("pw"); err != nil {
		t.Fatal(err)
	}
	if st := wantPhase(t, s, PhaseRunning); st.Name != "台式机" {
		t.Fatalf("state = %+v", st)
	}
}

// 运行中加入已有的设备圈：笔记本开放配对，台式机（已经是单机圈）输入配对码和笔记本的地址。
func TestJoinWhileRunning(t *testing.T) {
	useFileKeystore(t)
	laptop := runningService(t, "笔记本")
	ln, _ := laptop.node()
	code, _ := pairing.NewCode()
	sid, err := ln.Store().CreatePairSession(code, pairing.CodeTTL)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // 等守护进程的下一次轮询开放配对

	desktop := runningService(t, "台式机")
	if _, err := desktop.Join("0000-0000", ln.Addr()); err == nil || err.Error() != "配对码不正确" {
		t.Fatalf("wrong code: err = %v", err)
	}
	wantPhase(t, desktop, PhaseRunning)
	// 邀请方同一时间只处理一次配对尝试：等它记下上一次失败，再用正确的配对码重试。
	eventually(t, "failed attempt recorded", func() bool {
		ps, err := ln.Store().GetPairSession(sid)
		return err == nil && ps.Attempts == 1
	})
	res, err := desktop.Join(pairing.FormatCode(code), ln.Addr())
	if err != nil || res.Mode != "join" || res.PeerName != "笔记本" || res.Devices != 2 || len(res.Waiting) != 0 {
		t.Fatalf("Join = %+v, %v", res, err)
	}
	wantPhase(t, desktop, PhaseRunning)
	if names := devicesOf(t, desktop); len(names) != 2 {
		t.Fatalf("devices = %v", names)
	}
}

// 本机的设备圈里有其他设备、对方是单机：对方换到本机的圈（ADOPT），手机的配对记录里记下这个模式。
func TestJoinAdoptsSoloInviter(t *testing.T) {
	useFileKeystore(t)
	laptop := runningService(t, "笔记本")
	desktop := runningService(t, "台式机")
	joinVia(t, laptop, desktop)
	phone := runningService(t, "手机")
	p, err := phone.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	pn, _ := phone.node()
	time.Sleep(200 * time.Millisecond) // 等守护进程的下一次轮询开放配对

	res, err := desktop.Join(p.Code, pn.Addr())
	if err != nil || res.Mode != "adopt" || res.PeerName != "手机" || res.Devices != 3 || len(res.Waiting) != 0 {
		t.Fatalf("Join = %+v, %v", res, err)
	}
	eventually(t, "phone pairing done", func() bool {
		got, err := phone.PairingStatus(p.ID)
		return err == nil && got.Status == PairDone
	})
	got, _ := phone.PairingStatus(p.ID)
	if got.Mode != "adopt" || got.JoinerName != "台式机" {
		t.Fatalf("PairingStatus = %+v", got)
	}
	eventually(t, "phone joined the desktop's circle", func() bool { return len(devicesOf(t, phone)) == 3 })
}

// 两边的设备圈里都有其他设备：服务层的 Join 总是同意合并，两个圈合成一个。
func TestJoinMultiIntoMultiMerges(t *testing.T) {
	useFileKeystore(t)
	laptop := runningService(t, "笔记本")
	desktop := runningService(t, "台式机")
	joinVia(t, laptop, desktop)
	phone := runningService(t, "手机")
	tablet := runningService(t, "平板")
	joinVia(t, phone, tablet)

	n, _ := phone.node()
	code, _ := pairing.NewCode()
	if _, err := n.Store().CreatePairSession(code, pairing.CodeTTL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := desktop.Join(code, n.Addr()); err != nil {
		t.Fatalf("Join: %v", err)
	}
	wantPhase(t, desktop, PhaseRunning)
	if names := devicesOf(t, desktop); len(names) != 4 {
		t.Fatalf("devices after merge = %v", names)
	}
}

// 合并：台式机所在的圈有 3 台、手机所在的圈有 2 台。手机开放配对、台式机输入配对码，台式机换到手机的圈，
// 另外两台还没跟过来：Join 的结果、邀请方的配对记录、Join 方的设备列表里都能看到。
func TestServiceMerge(t *testing.T) {
	useFileKeystore(t)
	laptop := runningService(t, "笔记本")
	desktop := runningService(t, "台式机")
	joinVia(t, laptop, desktop)
	nas := runningService(t, "NAS")
	joinVia(t, nas, desktop) // 台式机所在的圈：笔记本、台式机、NAS
	phone := runningService(t, "手机")
	tablet := runningService(t, "平板")
	joinVia(t, phone, tablet)

	p, err := phone.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	pn, _ := phone.node()
	time.Sleep(200 * time.Millisecond) // 等守护进程的下一次轮询开放配对
	res, err := desktop.Join(p.Code, pn.Addr())
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if res.Mode != "merge" || res.PeerName != "手机" || res.Devices != 5 {
		t.Fatalf("Join = %+v", res)
	}
	waiting := append([]string(nil), res.Waiting...)
	sort.Strings(waiting)
	if len(waiting) != 2 || waiting[0] != "NAS" || waiting[1] != "笔记本" {
		t.Fatalf("Waiting = %v", res.Waiting)
	}
	if res.Outdated == nil || len(res.Outdated) != 0 {
		t.Fatalf("Outdated = %#v", res.Outdated)
	}
	eventually(t, "phone pairing done", func() bool {
		got, err := phone.PairingStatus(p.ID)
		return err == nil && got.Status == PairDone
	})
	got, _ := phone.PairingStatus(p.ID)
	if got.Mode != "merge" || got.Brought != 2 || got.JoinerName != "台式机" {
		t.Fatalf("PairingStatus = %+v", got)
	}
	devs, err := desktop.Devices()
	if err != nil {
		t.Fatal(err)
	}
	moving := 0
	for _, d := range devs {
		if d.Move == "waiting" && (d.Name == "NAS" || d.Name == "笔记本") && d.MoveUntil > time.Now().UnixMilli() {
			moving++
		} else if d.Move != "" {
			t.Fatalf("unexpected move state: %+v", d)
		}
	}
	if moving != 2 {
		t.Fatalf("devices = %+v", devs)
	}
}

func TestUIErrorMergeUnsupported(t *testing.T) {
	if got := uiError(pairing.ErrMergeUnsupported).Error(); got != "对方的 Portide 版本不支持合并设备圈，请先在那台设备上升级" {
		t.Fatalf("uiError = %q", got)
	}
}

// 旧版本留下的数据目录有身份和名字、没有设备圈：启动时自动建好单机圈。
func TestBootCreatesCircleForOldSetupDir(t *testing.T) {
	dir, keys := t.TempDir(), keystore.Memory{}
	if _, err := node.Init(dir, keys, "台式机"); err != nil {
		t.Fatal(err)
	}
	s := New(Config{DataDir: dir, Keys: keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true, PollInterval: 50 * time.Millisecond, Logf: t.Logf})
	t.Cleanup(func() { Stop(s) })
	if st := wantPhase(t, s, PhaseRunning); st.Name != "台式机" {
		t.Fatalf("state = %+v", st)
	}
	if names := devicesOf(t, s); len(names) != 1 {
		t.Fatalf("devices = %v", names)
	}
}

// 本机正在开放配对时，浏览到的是自己发布的：返回 false（立即返回，不浏览）。
func TestNearbyInvitersFalseWhilePairingPending(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "台式机")
	if _, err := s.StartPairing(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if s.NearbyInviters() {
		t.Fatal("NearbyInviters must be false while this device is inviting")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v, want an immediate return", took)
	}
}

// 关闭了局域网发现时，附近不会有可发现的邀请方。
func TestNearbyInvitersFalseWithoutMDNS(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "台式机")
	if s.NearbyInviters() {
		t.Fatal("NearbyInviters must be false without mDNS")
	}
}

// 数据目录已经有别的守护进程在运行时不启动核心；对方停止后"重试"就能启动。
func TestBusyWhileAnotherDaemonRuns(t *testing.T) {
	useFileKeystore(t)
	t.Setenv("PORTIDE_PASSPHRASE", "pw") // 模拟命令行：口令来自环境变量
	dir := t.TempDir()
	keys, err := keystore.NewFile(keysFile(dir), "pw", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := node.Init(dir, keys, "台式机"); err != nil {
		t.Fatal(err)
	}
	other, err := node.Open(node.Options{DataDir: dir, Keys: keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = other.Run(ctx); close(done) }()
	<-other.Ready()

	s := newService(t, dir)
	if st := wantPhase(t, s, PhaseBusy); st.Error == "" {
		t.Fatal("busy must explain why")
	}
	cancel()
	<-done
	other.Close()
	if st := s.Retry(); st.Phase != PhaseRunning {
		t.Fatalf("after the other daemon stopped: %+v", st)
	}
}

// 监听失败（端口被占用）之后，核心所在的 Node 被丢弃；端口空出来之后"重试"要能重新打开它并启动。
func TestRetryRecoversAfterListenFailure(t *testing.T) {
	useFileKeystore(t)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()

	s := New(Config{DataDir: t.TempDir(), ListenAddr: addr, DisableMDNS: true, PollInterval: 50 * time.Millisecond, Logf: t.Logf})
	t.Cleanup(func() { Stop(s) })
	if err := s.Unlock("pw"); err != nil {
		t.Fatal(err)
	}
	if err := s.Init("台式机"); err == nil {
		t.Fatal("want a listen failure while the port is occupied")
	}
	if st := wantPhase(t, s, PhaseError); st.Error == "" {
		t.Fatal("error phase must explain why")
	}
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
	if st := s.Retry(); st.Phase != PhaseRunning {
		t.Fatalf("after freeing the port: %+v", st)
	}
}

// 监听失败那一刻（startLocked 短暂解锁等 Run 退出）如果正好有人并发调用 Stop：曾经会在
// 恢复执行之后对着已经被 Stop 清空的 s.n 再 Close 一次，panic 在 nil 指针上。
// 这里让两件事真的同时发生：触发监听失败的 Init 和 Stop 一起跑，用 -race 验证
// 既不 panic 也没有数据竞争。
func TestStopDuringListenFailureDoesNotPanic(t *testing.T) {
	useFileKeystore(t)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	addr := pc.LocalAddr().String()

	s := New(Config{DataDir: t.TempDir(), ListenAddr: addr, DisableMDNS: true, PollInterval: 50 * time.Millisecond, Logf: t.Logf})
	t.Cleanup(func() { Stop(s) }) // Stop 可以重复调用
	if err := s.Unlock("pw"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = s.Init("台式机") }() // 端口被占用：会走到监听失败那条路
	go func() { defer wg.Done(); Stop(s) }()
	wg.Wait()
}

// 停止之后再调用绑定的方法（例如窗口关闭时界面还在刷新）：返回 ErrNotRunning，不能对着已经关掉的核心解引用 nil。
func TestCallsAfterStopReturnErrNotRunning(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "台式机")
	Stop(s)
	if _, err := s.Devices(); err != ErrNotRunning {
		t.Fatalf("Devices after Stop: err = %v", err)
	}
	if _, err := s.Timeline("", 10); err != ErrNotRunning {
		t.Fatalf("Timeline after Stop: err = %v", err)
	}
	if st := s.State(); st.Phase != PhaseError || st.Error != "Portide 已停止" {
		t.Fatalf("state after Stop = %+v", st)
	}
	if st := s.Retry(); st.Phase != PhaseError {
		t.Fatalf("Retry after Stop must not restart: %+v", st)
	}
}

// Stop 不触发启动：从没被调用过任何方法的服务（例如单实例检查里退出的第二个实例）停止时不碰数据目录。
func TestStopDoesNotBoot(t *testing.T) {
	useFileKeystore(t)
	dir := t.TempDir()
	s := New(Config{DataDir: dir, ListenAddr: "127.0.0.1:0", DisableMDNS: true, Logf: t.Logf})
	Stop(s)
	if _, err := os.Stat(filepath.Join(dir, "portide.db")); !os.IsNotExist(err) {
		t.Fatalf("Stop must not boot; portide.db stat err = %v", err)
	}
}

// 口令来自环境变量 PORTIDE_PASSPHRASE 且不对时：界面里输入口令也没用（环境变量优先），所以报错而不是停在输入口令。
func TestWrongEnvPassphraseIsAnError(t *testing.T) {
	useFileKeystore(t)
	dir := t.TempDir()
	keys, err := keystore.NewFile(keysFile(dir), "pw", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := node.Init(dir, keys, "台式机"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTIDE_PASSPHRASE", "bad")
	s := newService(t, dir)
	if st := wantPhase(t, s, PhaseError); st.Error != "环境变量 PORTIDE_PASSPHRASE 的口令不对" {
		t.Fatalf("state = %+v", st)
	}
}

// 加入设备圈的过程中（局域网里找邀请方要几秒到几十秒），离开设备圈、再一次加入都不能和它一起改动设备圈。
func TestJoinIsExclusive(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "台式机")

	joinErr := make(chan error, 1)
	go func() { _, err := s.Join("0000-0000", "127.0.0.1:1"); joinErr <- err }() // 没有人应答的地址：会一直停在拨号里

	eventually(t, "Join occupies the joining flag", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.joining
	})
	if _, err := s.Join("0000-0000", "127.0.0.1:1"); err == nil || err.Error() != "正在加入设备圈，请稍候" {
		t.Fatalf("Join while joining: err = %v", err)
	}
	if _, err := s.LeaveCircle(false); err == nil || err.Error() != "正在加入设备圈，请稍候" {
		t.Fatalf("LeaveCircle while joining: err = %v", err)
	}

	if err := <-joinErr; err == nil {
		t.Fatal("want the join to fail dialing an address nobody answers")
	}
	wantPhase(t, s, PhaseRunning)
}

// runningService 返回一个设备名已设好、自动建好单机圈、核心正在运行的服务。
func runningService(t *testing.T, name string) *Service {
	t.Helper()
	s := newService(t, t.TempDir())
	if err := s.Unlock("pw"); err != nil {
		t.Fatal(err)
	}
	if err := s.Init(name); err != nil {
		t.Fatal(err)
	}
	wantPhase(t, s, PhaseRunning)
	isolateSaveDir(t, s)
	return s
}

// isolateSaveDir 把收到的文件放进临时目录：不设置时会落到用户真实的"下载/Portide"里。
func isolateSaveDir(t *testing.T, s *Service) {
	t.Helper()
	n, err := s.node()
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SetSaveDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 端口被占用：出错页的原因分类是 port-in-use；"换一个端口启动"把端口改成自动选择并启动成功。
func TestUseAutomaticPort(t *testing.T) {
	useFileKeystore(t)
	busy, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.LocalAddr().(*net.UDPAddr).Port

	// 先用核心建好身份并把端口设成被占用的那个（Service.Init 会马上启动核心，来不及设置），再交给服务启动。
	dir, keys := t.TempDir(), keystore.Memory{}
	if _, err := node.Init(dir, keys, "台式机"); err != nil {
		t.Fatal(err)
	}
	pn, err := node.Open(node.Options{DataDir: dir, Keys: keys, DisableMDNS: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := pn.SetListenPort(busyPort); err != nil {
		t.Fatal(err)
	}
	_ = pn.Close()
	s := New(Config{DataDir: dir, Keys: keys, DisableMDNS: true, PollInterval: 50 * time.Millisecond, Logf: t.Logf}) // 不指定地址：用端口设置
	t.Cleanup(func() { Stop(s) })
	st := wantPhase(t, s, PhaseError)
	if st.ErrorKind != ErrorKindPortInUse || !strings.HasPrefix(st.Error, "端口已被占用") || st.Detail == "" || st.DataDir == "" {
		t.Fatalf("error state = %+v", st)
	}
	if st := s.UseAutomaticPort(); st.Phase != PhaseRunning || st.ErrorKind != "" {
		t.Fatalf("after UseAutomaticPort: %+v", st)
	}
	set, err := s.Settings()
	if err != nil || set.ListenPort != 0 || set.ActualPort == 0 || set.ActualPort == busyPort {
		t.Fatalf("settings = %+v %v, want an automatic port", set, err)
	}
}

// 两台都是单机圈：台式机离开后成为一个新的单机圈（圈子 ID 变了），再加入笔记本的设备圈。
func TestLeaveSoloCircleThenJoin(t *testing.T) {
	useFileKeystore(t)
	laptop := runningService(t, "笔记本")
	desktop := runningService(t, "台式机")

	before := circleOf(t, desktop)
	time.Sleep(1100 * time.Millisecond) // 圈子 ID 是创世记录的哈希，记录里的时间只精确到秒：不隔开就会生成一模一样的 ID
	res, err := desktop.LeaveCircle(false)
	if err != nil || res != (LeaveResult{}) {
		t.Fatalf("LeaveCircle = %+v, %v", res, err)
	}
	if st := wantPhase(t, desktop, PhaseRunning); st.Name != "台式机" {
		t.Fatalf("after leaving: %+v", st)
	}
	wantSolo(t, desktop, before)
	joinVia(t, laptop, desktop)
}

// joinVia 让 joiner（running 阶段）用 inviter 开放的配对码加入它的设备圈。
func joinVia(t *testing.T, inviter, joiner *Service) {
	t.Helper()
	n, _ := inviter.node()
	code, _ := pairing.NewCode()
	if _, err := n.Store().CreatePairSession(code, pairing.CodeTTL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // 等守护进程的下一次轮询开放配对
	if _, err := joiner.Join(code, n.Addr()); err != nil {
		t.Fatal(err)
	}
	wantPhase(t, joiner, PhaseRunning)
}

// circleOf 返回服务当前所在设备圈的 ID。
func circleOf(t *testing.T, s *Service) string {
	t.Helper()
	n, err := s.node()
	if err != nil {
		t.Fatal(err)
	}
	return n.CircleID()
}

// wantSolo 检查服务在运行、设备圈里只有本机，而且已经不是 before 那个设备圈。
func wantSolo(t *testing.T, s *Service, before string) {
	t.Helper()
	if names := devicesOf(t, s); len(names) != 1 {
		t.Fatalf("devices after leaving = %v, want only this device", names)
	}
	if after := circleOf(t, s); after == "" || after == before {
		t.Fatalf("circle id %q, want a new one (was %q)", after, before)
	}
}

func devicesOf(t *testing.T, s *Service) []string {
	t.Helper()
	devs, err := s.Devices()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range devs {
		names = append(names, d.Name)
	}
	return names
}

// 台式机已经在笔记本的设备圈里，改为加入手机的：笔记本把它移出去，台式机成为新的单机圈，再加入手机的设备圈。
func TestLeaveCircleWithOtherDevices(t *testing.T) {
	useFileKeystore(t)
	laptop := runningService(t, "笔记本")
	desktop := runningService(t, "台式机")
	joinVia(t, laptop, desktop)
	phone := runningService(t, "手机")
	eventually(t, "laptop online for the desktop", func() bool {
		devs, _ := desktop.Devices()
		return len(devs) == 2 && devs[1].Online
	})
	// 在线状态先于对方功能写进数据库：等台式机记下笔记本支持 leave。
	eventually(t, "laptop features recorded", func() bool {
		dn, err := desktop.node()
		if err != nil {
			return false
		}
		ln, err := laptop.node()
		if err != nil {
			return false
		}
		known, _ := dn.Store().PeerFeatures()
		return slices.Contains(known[ln.Identity().KeyID], node.FeatureLeave)
	})

	before := circleOf(t, desktop)
	res, err := desktop.LeaveCircle(false)
	if err != nil || res.NeedsForce || res.RemovedBy != "笔记本" {
		t.Fatalf("LeaveCircle = %+v, %v", res, err)
	}
	wantPhase(t, desktop, PhaseRunning)
	wantSolo(t, desktop, before)
	eventually(t, "laptop removed the desktop", func() bool { return len(devicesOf(t, laptop)) == 1 })

	joinVia(t, phone, desktop)
	if names := devicesOf(t, desktop); len(names) != 2 || names[1] != "手机" {
		t.Fatalf("desktop devices = %v", names)
	}
}

// 其他设备都不在线：不离开，返回 NeedsForce；带 force 再调就只在本机离开，其他设备照旧把它当成员。
func TestLeaveCircleNeedsForceWhenNobodyCanHelp(t *testing.T) {
	useFileKeystore(t)
	laptop := runningService(t, "笔记本")
	desktop := runningService(t, "台式机")
	joinVia(t, laptop, desktop)
	Stop(laptop)

	res, err := desktop.LeaveCircle(false)
	if err != nil || !res.NeedsForce || !strings.Contains(res.Reason, "不在线") {
		t.Fatalf("LeaveCircle = %+v, %v", res, err)
	}
	wantPhase(t, desktop, PhaseRunning)
	if names := devicesOf(t, desktop); len(names) != 2 {
		t.Fatalf("a refused leave changed the circle: %v", names)
	}

	before := circleOf(t, desktop)
	if res, err := desktop.LeaveCircle(true); err != nil || res != (LeaveResult{}) {
		t.Fatalf("forced LeaveCircle = %+v, %v", res, err)
	}
	wantPhase(t, desktop, PhaseRunning)
	wantSolo(t, desktop, before)
}

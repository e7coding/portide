package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"portide/core/keystore"
	"portide/core/node"
	"portide/core/store"
)

// iosService 按 iOS 的方式创建服务：注入密钥库和保存目录，不用口令（M4）。
func iosService(t *testing.T, name string) (*Service, string) {
	t.Helper()
	t.Setenv("PORTIDE_KEYSTORE", "file") // 注入了 Keys 时这些设置都不该起作用
	t.Setenv("PORTIDE_PASSPHRASE", "")
	saveDir := filepath.Join(t.TempDir(), "Documents")
	s := New(Config{
		DataDir: t.TempDir(), Keys: keystore.Memory{}, SaveDir: saveDir,
		ListenAddr: "127.0.0.1:0", DisableMDNS: true, PollInterval: 50 * time.Millisecond, Logf: t.Logf,
	})
	t.Cleanup(func() { Stop(s) })
	wantPhase(t, s, PhaseSetup)
	if err := s.Init(name); err != nil {
		t.Fatal(err)
	}
	wantPhase(t, s, PhaseRunning)
	return s, saveDir
}

// 注入密钥库时没有"设置口令"这一步；保存目录每次打开核心都设成 Config.SaveDir。
func TestInjectedKeysAndSaveDir(t *testing.T) {
	s, saveDir := iosService(t, "iPhone")
	got, err := s.Settings()
	if err != nil || got.SaveDir != saveDir {
		t.Fatalf("Settings = %+v, %v; want save dir %q", got, err, saveDir)
	}
	// 用户（或旧版本）改过保存目录：下次打开核心时又被设回 Config.SaveDir。
	if err := s.SetSaveDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	Suspend(s)
	if st := Resume(s); st.Phase != PhaseRunning {
		t.Fatalf("Resume = %+v", st)
	}
	if got, _ := s.Settings(); got.SaveDir != saveDir {
		t.Fatalf("save dir after resume = %q, want %q", got.SaveDir, saveDir)
	}
}

// 退到后台时暂停：核心关闭、方法返回 ErrNotRunning；回到前台恢复后数据都在。
func TestSuspendAndResume(t *testing.T) {
	s, _ := iosService(t, "iPhone")
	if err := s.SetHistoryDays(30); err != nil {
		t.Fatal(err)
	}
	Suspend(s)
	Suspend(s) // 重复调用没有影响
	if st := s.State(); st.Phase != PhaseSuspended {
		t.Fatalf("phase after Suspend = %+v", st)
	}
	if _, err := s.Timeline("", 10); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Timeline while suspended: %v", err)
	}
	st := Resume(s)
	if st.Phase != PhaseRunning || st.Name != "iPhone" {
		t.Fatalf("Resume = %+v", st)
	}
	if got, _ := s.Settings(); got.HistoryDays != 30 {
		t.Fatalf("settings lost across suspend: %+v", got)
	}
	// Stop 之后 Resume 不会重新启动。
	Stop(s)
	if st := Resume(s); st.Phase == PhaseRunning {
		t.Fatalf("Resume after Stop = %+v", st)
	}
}

// 发件箱：还在排队的任务引用的副本保留，其余删除。
func TestCleanOutbox(t *testing.T) {
	laptop, desktop := pairedServices(t)
	target := keyOf(t, desktop)
	Stop(desktop)
	eventually(t, "desktop offline", func() bool { return !online(t, laptop, "台式机") })

	outbox := filepath.Join(t.TempDir(), "outbox")
	keepDir := filepath.Join(outbox, "keep")
	oldDir := filepath.Join(outbox, "old")
	for _, d := range []string{keepDir, oldDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	keepFile := filepath.Join(keepDir, "照片.jpg")
	if err := os.WriteFile(keepFile, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "旧.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.SendFiles([]string{target}, []string{keepFile}); err != nil {
		t.Fatal(err)
	}
	if err := CleanOutbox(laptop, outbox); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("queued file removed: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("stale outbox entry kept: %v", err)
	}
	if err := CleanOutbox(laptop, filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("missing outbox: %v", err)
	}
	if err := CleanOutbox(desktop, outbox); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("CleanOutbox on a stopped service: %v", err)
	}
}

// 设置了 Config.SaveDir 时，收到的文件按当前保存目录定位（iOS 更新后容器路径会变）；发出的文件不受影响。
func TestLocalPathFollowsSaveDir(t *testing.T) {
	s := &Service{cfg: Config{SaveDir: "/new/Documents"}}
	in := node.HistoryEntry{Message: store.Message{Direction: store.MessageIn}, Body: node.HistoryBody{Dir: "/old/Documents"}}
	if got := s.localPath(in, node.HistoryFile{Path: "照片/a.jpg"}); got != filepath.Join("/new/Documents", "照片", "a.jpg") {
		t.Fatalf("incoming path = %q", got)
	}
	out := node.HistoryEntry{Message: store.Message{Direction: store.MessageOut}}
	if got := s.localPath(out, node.HistoryFile{Path: "a.jpg", Local: "/outbox/x/a.jpg"}); got != "/outbox/x/a.jpg" {
		t.Fatalf("outgoing path = %q", got)
	}
	if got := (&Service{}).localPath(in, node.HistoryFile{Path: "a.jpg"}); got != filepath.Join("/old/Documents", "a.jpg") {
		t.Fatalf("path without SaveDir = %q", got)
	}
}

// iOS 删掉应用后 Keychain 里的设备密钥还在，重装后的数据目录却是新的（没有设备名）：
// 这时仍然要先起名字，Init 只补上名字；否则加入设备圈时发出去的名字是空的，邀请方会拒绝。
func TestReinstallWithKeysButNoNameAsksForName(t *testing.T) {
	t.Setenv("PORTIDE_KEYSTORE", "file")
	t.Setenv("PORTIDE_PASSPHRASE", "")
	keys := keystore.Memory{}
	first := New(Config{DataDir: t.TempDir(), Keys: keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true, PollInterval: 50 * time.Millisecond, Logf: t.Logf})
	wantPhase(t, first, PhaseSetup)
	if err := first.Init("旧手机"); err != nil {
		t.Fatal(err)
	}
	Stop(first)

	s := New(Config{DataDir: t.TempDir(), Keys: keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true, PollInterval: 50 * time.Millisecond, Logf: t.Logf})
	t.Cleanup(func() { Stop(s) })
	if st := wantPhase(t, s, PhaseSetup); !st.NeedsName {
		t.Fatalf("keys without a stored name must still ask for a name: %+v", st)
	}
	if err := s.Init("我的 iPhone"); err != nil {
		t.Fatal(err)
	}
	if err := s.Init("再来一次"); err == nil {
		t.Fatal("a named device must not be initialised again")
	}
	if st := wantPhase(t, s, PhaseRunning); st.Name != "我的 iPhone" {
		t.Fatalf("running: %+v", st)
	}
}

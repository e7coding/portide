package app

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"portide/core/relay"
)

func TestSettingsRoundTrip(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "笔记本")
	dir := filepath.Join(t.TempDir(), "收到的文件")
	if err := s.SetSaveDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReceivePolicy("ask-above", 100<<20); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHistoryDays(0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetQueueDays(30); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got.ActualPort == 0 || got.ListenPort != 7443 {
		t.Fatalf("ports = %d (listening on %d), want the default setting and a real port", got.ListenPort, got.ActualPort)
	}
	want := Settings{SaveDir: dir, ReceiveMode: "ask-above", AskAbove: 100 << 20, HistoryDays: 0, QueueDays: 30, Notify: true,
		ListenPort: 7443, ActualPort: got.ActualPort}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
	if err := s.SetNotify(false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Settings(); got.Notify {
		t.Fatal("notify still on")
	}

	for name, err := range map[string]error{
		"empty save dir":    s.SetSaveDir(" "),
		"bad policy":        s.SetReceivePolicy("sometimes", 0),
		"zero threshold":    s.SetReceivePolicy("ask-above", 0),
		"history too long":  s.SetHistoryDays(4000),
		"queue days zero":   s.SetQueueDays(0),
		"queue days too ln": s.SetQueueDays(400),
	} {
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if count, err := s.ClearHistory(); err != nil || count != 0 {
		t.Fatalf("ClearHistory = %d, %v", count, err)
	}
}

func relayCfg(addr string, fp, tok byte) relay.Config {
	cfg := relay.Config{Addr: addr, Token: make([]byte, relay.TokenSize)}
	cfg.Fingerprint[0] = fp
	cfg.Token[0] = tok
	return cfg
}

func TestServiceRelayList(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "笔记本")
	info, err := s.Relay()
	if err != nil || len(info.Relays) != 0 || info.ChangedBy != "" || info.ChangedAt != 0 {
		t.Fatalf("no relay yet: %+v, %v", info, err)
	}
	a, b := relayCfg("a.example.com:7443", 0xAB, 1), relayCfg("b.example.com:7443", 0xCD, 1)
	if err := s.AddRelay("  portide relay add " + a.ConfigString() + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRelay(b.ConfigString()); err != nil {
		t.Fatal(err)
	}
	info, err = s.Relay()
	if err != nil || len(info.Relays) != 2 || info.Relays[0].Addr != a.Addr || info.Relays[1].Addr != b.Addr {
		t.Fatalf("relays = %+v, %v", info, err)
	}
	if e := info.Relays[0]; !strings.HasPrefix(e.Fingerprint, "ab") || len(e.Fingerprint) != 16 || len(e.ID) != 64 || e.Status != "正在连接" {
		t.Fatalf("entry = %+v", e)
	}
	if info.ChangedBy != "笔记本" || info.ChangedAt == 0 {
		t.Fatalf("changed = %q %d", info.ChangedBy, info.ChangedAt)
	}
	// 同一指纹换令牌：更新而不是新增。
	if err := s.AddRelay(relayCfg("a.example.com:7443", 0xAB, 2).ConfigString()); err != nil {
		t.Fatal(err)
	}
	if info, _ = s.Relay(); len(info.Relays) != 2 {
		t.Fatalf("after token update: %+v", info.Relays)
	}
	// 加满 4 个后再加报错。
	for i, fp := range []byte{3, 4} {
		if err := s.AddRelay(relayCfg(fmt.Sprintf("c%d.example.com:7443", i), fp, 1).ConfigString()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddRelay(relayCfg("e.example.com:7443", 5, 1).ConfigString()); err == nil || err.Error() != "最多 4 个中继：先删掉一个" {
		t.Fatalf("full: err = %v", err)
	}
	if err := s.AddRelay("not a config"); err == nil || !strings.HasPrefix(err.Error(), "配置串无效") {
		t.Fatalf("bad config: err = %v", err)
	}
	// 按 id 删除；不存在的 id 报中文错误。
	if err := s.RemoveRelay("zz"); err == nil || err.Error() != "没有这个中继" {
		t.Fatalf("bad id: err = %v", err)
	}
	info, _ = s.Relay()
	if err := s.RemoveRelay(info.Relays[0].ID); err != nil {
		t.Fatal(err)
	}
	if info, _ = s.Relay(); len(info.Relays) != 3 || info.Relays[0].Addr != b.Addr {
		t.Fatalf("after remove: %+v", info.Relays)
	}
	if err := s.RemoveRelay(hex.EncodeToString(a.Fingerprint[:])); err == nil || err.Error() != "没有这个中继" {
		t.Fatalf("removed again: err = %v", err)
	}
}

// 核心没有运行时，数据方法一律返回 ErrNotRunning，不会碰空指针。
func TestMethodsBeforeRunning(t *testing.T) {
	useFileKeystore(t)
	s := newService(t, t.TempDir())
	if _, err := s.Timeline("", 10); err != ErrNotRunning {
		t.Fatalf("Timeline: %v", err)
	}
	if _, err := s.Devices(); err != ErrNotRunning {
		t.Fatalf("Devices: %v", err)
	}
	if _, err := s.Settings(); err != ErrNotRunning {
		t.Fatalf("Settings: %v", err)
	}
	if _, err := s.StartPairing(); err != ErrNotRunning {
		t.Fatalf("StartPairing: %v", err)
	}
	if _, err := s.Transfers(); err != ErrNotRunning {
		t.Fatalf("Transfers: %v", err)
	}
	if err := s.PauseSend(1); err != ErrNotRunning {
		t.Fatalf("PauseSend: %v", err)
	}
}

func TestOpenSaveDirAndLocalAddresses(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "笔记本")
	var opened string
	s.cfg.Open = func(p string) error { opened = p; return nil }
	set, _ := s.Settings()
	if err := s.OpenSaveDir(); err != nil || opened != set.SaveDir {
		t.Fatalf("OpenSaveDir opened %q (%v), want %q", opened, err, set.SaveDir)
	}
	addrs, err := s.LocalAddresses()
	if err != nil || addrs == nil {
		t.Fatalf("LocalAddresses = %v %v", addrs, err)
	}
	for _, a := range addrs {
		if ap, err := netip.ParseAddrPort(a); err != nil || int(ap.Port()) != set.ActualPort || ap.Addr().IsLoopback() {
			t.Errorf("address %q: want a non-loopback address with port %d", a, set.ActualPort)
		}
	}
}

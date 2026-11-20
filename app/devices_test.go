package app

import (
	"fmt"
	"testing"
	"time"

	"portide/core/pairing"
	"portide/core/relay"
	"portide/core/store"
)

func TestConnPath(t *testing.T) {
	for addr, want := range map[string]string{
		(relay.Addr{}).String(): PathRelay,
		"relay:1.2.3.4:7443":    PathRelay,
		"192.168.1.5:7443":      PathLAN,
		"10.0.0.2:7443":         PathLAN,
		"127.0.0.1:7443":        PathLAN,
		"[fe80::1%3]:7443":      PathLAN,
		"203.0.113.9:51000":     PathDirect,
		"[2001:db8::1]:7443":    PathDirect,
	} {
		if got := connPath(addr); got != want {
			t.Errorf("connPath(%q) = %q, want %q", addr, got, want)
		}
	}
}

// 配对成功后：设备列表里本机排第一，对方在线、经局域网（回环地址），配对会话显示加入的设备。
func TestDevicesAfterPairing(t *testing.T) {
	laptop, _ := pairedServices(t)
	devs, err := laptop.Devices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || !devs[0].Self || devs[0].Name != "笔记本" || devs[0].Path != "" {
		t.Fatalf("devices = %+v", devs)
	}
	if d := devs[1]; d.Name != "台式机" || !d.Online || d.Path != PathLAN || d.Addr == "" || d.LastSeen == 0 {
		t.Fatalf("peer = %+v", d)
	}
}

// 经中继的设备：Path 是 relay，Addr 是中继的地址（不带 relay: 前缀），界面据此显示"经中继（地址）"。
func TestDevicesRelayedAddr(t *testing.T) {
	laptop, _ := pairedServices(t)
	ln, _ := laptop.node()
	devs, _ := laptop.Devices()
	peer := devs[1]
	for addr, want := range map[string]string{"relay:1.2.3.4:7443": "1.2.3.4:7443", (relay.Addr{}).String(): ""} {
		if err := ln.Store().UpsertPeer(store.Peer{KeyID: peer.KeyID, Addr: addr, LastSeen: time.Now(), Online: true}); err != nil {
			t.Fatal(err)
		}
		devs, _ := laptop.Devices()
		if d := devs[1]; d.Path != PathRelay || d.Addr != want {
			t.Errorf("%q: path %q addr %q, want addr %q", addr, d.Path, d.Addr, want)
		}
	}
}

func TestPairingLifecycle(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "笔记本")
	p, err := s.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != PairWaiting || len(p.Code) != 9 || p.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatalf("pairing = %+v", p)
	}
	if err := s.CancelPairing(p.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PairingStatus(p.ID); got.Status != PairFailed {
		t.Fatalf("after cancel: %+v", got)
	}
	if _, err := s.PairingStatus(9999); err == nil {
		t.Fatal("an unknown pairing must fail")
	}
}

// 加入成功之后，邀请方看到的配对状态是 done，并带着新设备的名字。
func TestPairingReportsJoiner(t *testing.T) {
	laptop, _ := pairedServices(t)
	ln, _ := laptop.node()
	ps, err := ln.Store().GetPairSession(1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := laptop.PairingStatus(ps.ID)
	if err != nil || got.Status != PairDone || got.JoinerName != "台式机" {
		t.Fatalf("pairing = %+v, %v", got, err)
	}
}

// 只有要移除的设备在线时确认不了设备圈已同步：先返回 NeedsForce，带 force 才移除。
func TestRemoveDeviceNeedsForceWhenUnsure(t *testing.T) {
	laptop, desktop := pairedServices(t)
	if _, err := laptop.RemoveDevice(keyOf(t, laptop), false); err == nil {
		t.Fatal("removing this device itself must fail")
	}
	res, err := laptop.RemoveDevice(keyOf(t, desktop), false)
	if err != nil || !res.NeedsForce {
		t.Fatalf("without force: %+v, %v", res, err)
	}
	if devs, _ := laptop.Devices(); len(devs) != 2 {
		t.Fatal("NeedsForce must not remove anything")
	}
	res, err = laptop.RemoveDevice(keyOf(t, desktop), true)
	if err != nil || res.NeedsForce || res.RelayWarning {
		t.Fatalf("with force: %+v, %v", res, err)
	}
	if devs, _ := laptop.Devices(); len(devs) != 1 {
		t.Fatalf("devices after removal = %+v", devs)
	}
}

func TestUnexpectedJoinMessage(t *testing.T) {
	err := uiError(fmt.Errorf("node: %w", pairing.ErrUnexpectedJoin))
	if err == nil || err.Error() != "对方要求这台设备离开现在的设备圈，已拒绝" {
		t.Fatalf("uiError = %v", err)
	}
}

func TestStaleCircleMessage(t *testing.T) {
	err := uiError(fmt.Errorf("node: %w", pairing.ErrStaleCircle))
	if err == nil || err.Error() != "这台设备记下的设备圈比对方新，请让对方先和圈里其他设备同步后再试" {
		t.Fatalf("uiError = %v", err)
	}
}

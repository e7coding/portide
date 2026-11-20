package node

import (
	"slices"
	"testing"
	"time"

	"portide/core/relay"
	"portide/core/relayset"
	"portide/core/store"
)

// relayStrings 返回设备当前中继列表的配置串。
func relayStrings(d *testDevice) []string {
	rs, _ := d.n.Relays()
	var out []string
	for _, r := range rs {
		out = append(out, r.ConfigString())
	}
	return out
}

func relayVersion(d *testDevice) int64 {
	s, _ := d.n.RelaySettings()
	if s == nil {
		return -1
	}
	return int64(s.Version)
}

func relayRaw(d *testDevice) string {
	s, _ := d.n.RelaySettings()
	if s == nil {
		return ""
	}
	return string(s.Raw)
}

// setAddr 让 d 记下 o 的当前监听地址（没有局域网发现时，这是它找到 o 的唯一办法）。
func setAddr(t *testing.T, d, o *testDevice) {
	t.Helper()
	if err := d.n.Store().UpsertPeer(store.Peer{KeyID: o.n.id.KeyID, Addr: o.n.Addr()}); err != nil {
		t.Fatal(err)
	}
}

func waitRelays(t *testing.T, what string, d *testDevice, want ...relay.Config) {
	t.Helper()
	var w []string
	for _, c := range want {
		w = append(w, c.ConfigString())
	}
	eventually(t, what, func() bool { return slices.Equal(relayStrings(d), w) })
}

func TestRelaySettingsSyncOnConnect(t *testing.T) {
	a, b := newDevice(t, "A"), newDevice(t, "B")
	circleOf(t, a, b)
	b.start(t)
	eventually(t, "A and B online", func() bool { return online(t, a, b.n.id.KeyID) && online(t, b, a.n.id.KeyID) })
	r1 := fakeRelayCfg(1, "a")
	if _, err := a.n.AddRelay(r1); err != nil {
		t.Fatal(err)
	}
	waitRelays(t, "B gets [R1]", b, r1)
}

// C 停着时 A 改了设置；C 启动后只和 B 连接，设置经 B 转发过来。
func TestRelaySettingsReachOfflineDevice(t *testing.T) {
	a, b, c := newDevice(t, "A"), newDevice(t, "B"), newDevice(t, "C")
	stopA := circleOf(t, a, b, c)
	b.start(t)
	eventually(t, "A and B online", func() bool { return online(t, a, b.n.id.KeyID) })
	r1 := fakeRelayCfg(1, "a")
	if _, err := a.n.AddRelay(r1); err != nil {
		t.Fatal(err)
	}
	waitRelays(t, "B gets [R1]", b, r1)
	stopA()
	clearAddrs(t, c, a)
	setAddr(t, c, b)
	c.start(t)
	waitRelays(t, "C gets [R1] via B", c, r1)
}

// 两边停着时各加一个中继（都是版本 1）：连上后两边的 Raw 一致。
func TestRelaySettingsConcurrentEditsConverge(t *testing.T) {
	a, b := newDevice(t, "A"), newDevice(t, "B")
	stopA := circleOf(t, a, b)
	stopA()
	a.reopen(t)
	if _, err := a.n.AddRelay(fakeRelayCfg(1, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.n.AddRelay(fakeRelayCfg(2, "b")); err != nil {
		t.Fatal(err)
	}
	if relayVersion(a) != 1 || relayVersion(b) != 1 {
		t.Fatalf("precondition: versions = %d %d", relayVersion(a), relayVersion(b))
	}
	b.start(t)
	a.start(t)
	setAddr(t, b, a) // A 重新打开后换了端口，Addr 要守护进程起来之后才有
	setAddr(t, a, b)
	eventually(t, "same Raw on both", func() bool {
		return relayRaw(a) != "" && relayRaw(a) == relayRaw(b)
	})
	time.Sleep(300 * time.Millisecond)
	if relayRaw(a) != relayRaw(b) {
		t.Fatal("settings diverged again")
	}
}

func TestRelaySettingsFromRemovedSignerRejected(t *testing.T) {
	logs := &logRecorder{}
	laptop, _, phone := circleOfThree(t, logs)
	if err := laptop.n.RemoveDevice(phone.n.id.KeyID); err != nil && err != ErrRemovedKeyPending {
		t.Fatal(err)
	}
	s, err := relayset.Sign(phone.n.id.Private, 99, []relay.Config{fakeRelayCfg(1, "a")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	laptop.n.handleRelaySettings(streamPeer{KeyID: "x", Name: "台式机"}, s.Raw)
	if got, _ := laptop.n.RelaySettings(); got != nil {
		t.Fatalf("settings must stay empty, got %+v", got)
	}
	if !logs.contains("中继设置无效") {
		t.Fatal("expected a log line about invalid relay settings")
	}
}

func TestRelaySettingsNotSentToOldPeer(t *testing.T) {
	a, b := newDevice(t, "A"), newDevice(t, "B")
	b.n.features = slices.DeleteFunc(slices.Clone(features), func(f string) bool { return f == FeatureRelays })
	circleOf(t, a, b)
	b.start(t)
	eventually(t, "A and B online", func() bool { return online(t, a, b.n.id.KeyID) && online(t, b, a.n.id.KeyID) })
	if _, err := a.n.AddRelay(fakeRelayCfg(1, "a")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if s, _ := b.n.RelaySettings(); s != nil {
		t.Fatalf("old peer must not receive settings, got %+v", s)
	}
}

// 守护进程在跑时，别的进程（命令行）改了中继：下一次轮询推给成员。
func TestRelayEditFromOtherProcessSyncs(t *testing.T) {
	a, b := newDevice(t, "A"), newDevice(t, "B")
	circleOf(t, a, b)
	b.start(t)
	eventually(t, "A and B online", func() bool { return online(t, a, b.n.id.KeyID) && online(t, b, a.n.id.KeyID) })
	cli, err := Open(Options{DataDir: a.dir, Keys: a.keys, DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	r1 := fakeRelayCfg(1, "a")
	if _, err := cli.AddRelay(r1); err != nil {
		t.Fatal(err)
	}
	waitRelays(t, "B gets [R1] from the other process's edit", b, r1)
}

// 删掉最后一个中继：空列表也是带新版本号的设置，同步出去，不会被对方的旧列表覆盖回来。
func TestRemoveLastRelaySyncs(t *testing.T) {
	a, b := newDevice(t, "A"), newDevice(t, "B")
	circleOf(t, a, b)
	b.start(t)
	eventually(t, "A and B online", func() bool { return online(t, a, b.n.id.KeyID) && online(t, b, a.n.id.KeyID) })
	r1 := fakeRelayCfg(1, "a")
	if _, err := a.n.AddRelay(r1); err != nil {
		t.Fatal(err)
	}
	waitRelays(t, "B gets [R1]", b, r1)
	if err := a.n.RemoveRelay(r1.Fingerprint); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B's list is empty at A's version", func() bool {
		return len(relayStrings(b)) == 0 && relayVersion(b) == relayVersion(a) && relayVersion(a) == 2
	})
	time.Sleep(300 * time.Millisecond)
	if len(relayStrings(a)) != 0 || len(relayStrings(b)) != 0 {
		t.Fatal("the removed relay came back")
	}
}

// 两台设备都从升级前的 relay_config 迁移出 [R1]（版本 0）：第一次连上后都离开版本 0；
// 之后 A 删掉 R1，两边都变成空列表，B 重启、重新连上之后也不会把 R1 带回来。
func TestUpgradedRelayRemovalSticks(t *testing.T) {
	a, b := newDevice(t, "A"), newDevice(t, "B")
	stopA := circleOf(t, a, b)
	stopA()
	r1 := fakeRelayCfg(1, "a")
	for _, d := range []*testDevice{a, b} {
		if err := d.n.Store().SetSecretMeta(store.MetaRelayConfig, r1.ConfigString()); err != nil {
			t.Fatal(err)
		}
		d.reopen(t) // Open 时迁移成版本 0
		if relayVersion(d) != 0 || !slices.Equal(relayStrings(d), configStrings(r1)) {
			t.Fatalf("precondition: v%d %v", relayVersion(d), relayStrings(d))
		}
	}
	a.start(t)
	setAddr(t, b, a) // A 重新打开后换了端口：让 B 去拨 A（在 B 启动前写，免得盖掉在线状态）
	stopB := b.startStoppable(t)
	eventually(t, "both leave version 0 with the same settings", func() bool {
		return relayVersion(a) >= 1 && relayRaw(a) == relayRaw(b)
	})
	if err := a.n.RemoveRelay(r1.Fingerprint); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both empty at the same version", func() bool {
		return len(relayStrings(a)) == 0 && len(relayStrings(b)) == 0 && relayRaw(a) == relayRaw(b)
	})

	stopB()
	b.reopen(t)
	setAddr(t, b, a)
	b.start(t)
	eventually(t, "A and B online again", func() bool { return online(t, a, b.n.id.KeyID) && online(t, b, a.n.id.KeyID) })
	time.Sleep(500 * time.Millisecond)
	if len(relayStrings(a)) != 0 || len(relayStrings(b)) != 0 {
		t.Fatalf("the removed relay came back: A %v, B %v", relayStrings(a), relayStrings(b))
	}
}

// 换圈时 d.conns 里可能还有旧圈的连接：pushRelaySettings 只发给当前链的成员。
func TestRelaySettingsNotPushedToNonMember(t *testing.T) {
	a, b := newDevice(t, "A"), newDevice(t, "B")
	// B 不声明 relays：A 的广播不会发给它，下面只有直接调用的那一次。
	b.n.features = slices.DeleteFunc(slices.Clone(features), func(f string) bool { return f == FeatureRelays })
	circleOf(t, a, b)
	b.start(t)
	eventually(t, "A and B online", func() bool { return online(t, a, b.n.id.KeyID) && online(t, b, a.n.id.KeyID) })
	if _, err := a.n.AddRelay(fakeRelayCfg(1, "a")); err != nil {
		t.Fatal(err)
	}
	const stranger = "not-a-member"
	if err := a.n.Store().SetPeerFeatures(stranger, []string{FeatureRelays}); err != nil {
		t.Fatal(err)
	}
	mc := connPtr(a, b.n.id.KeyID)
	if mc == nil {
		t.Fatal("no connection to B")
	}
	a.n.pushRelaySettings(t.Context(), stranger, mc.conn)
	time.Sleep(time.Second)
	if s, _ := b.n.RelaySettings(); s != nil {
		t.Fatalf("settings must not be pushed for a non-member, B got %+v", s)
	}
}

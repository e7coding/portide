package node

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"portide/core/relay"
	"portide/core/relayset"
	"portide/core/store"
)

// fakeRelayCfg 造一个第 i 个中继的配置（指纹不同）。
func fakeRelayCfg(i byte, token string) relay.Config {
	c := relay.Config{Addr: "127.0.0.1:7443", Token: bytes.Repeat([]byte(token), relay.TokenSize)}
	c.Fingerprint[0] = i
	return c
}

func TestAddRelayUpsertAndFull(t *testing.T) {
	d := newDevice(t, "笔记本")
	if err := d.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	if s, err := d.n.RelaySettings(); err != nil || s != nil {
		t.Fatalf("initial settings = %v %v", s, err)
	}
	if updated, err := d.n.AddRelay(fakeRelayCfg(1, "a")); err != nil || updated {
		t.Fatalf("add R1 = %v %v", updated, err)
	}
	if s, _ := d.n.RelaySettings(); s == nil || s.Version != 1 || !bytes.Equal(s.Signer, d.n.id.Public) {
		t.Fatalf("settings after R1 = %+v", s)
	}
	if updated, err := d.n.AddRelay(fakeRelayCfg(1, "b")); err != nil || !updated {
		t.Fatalf("re-add R1 = %v %v", updated, err)
	}
	s, _ := d.n.RelaySettings()
	if s.Version != 2 || len(s.Configs) != 1 || string(s.Configs[0].Token) != string(bytes.Repeat([]byte("b"), relay.TokenSize)) {
		t.Fatalf("settings after token change = %+v", s)
	}
	for i := byte(2); i <= 4; i++ {
		if _, err := d.n.AddRelay(fakeRelayCfg(i, "x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.n.AddRelay(fakeRelayCfg(5, "x")); !errors.Is(err, relayset.ErrFull) {
		t.Fatalf("fifth relay err = %v", err)
	}
	if rs, _ := d.n.Relays(); len(rs) != 4 {
		t.Fatalf("relays = %d", len(rs))
	}
}

func TestRemoveRelay(t *testing.T) {
	d := newDevice(t, "笔记本")
	if err := d.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	r1 := fakeRelayCfg(1, "a")
	if err := d.n.RemoveRelay(r1.Fingerprint); !errors.Is(err, ErrRelayNotFound) {
		t.Fatalf("remove missing = %v", err)
	}
	if _, err := d.n.AddRelay(r1); err != nil {
		t.Fatal(err)
	}
	if err := d.n.RemoveRelay(r1.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if rs, err := d.n.Relays(); err != nil || len(rs) != 0 {
		t.Fatalf("relays = %v %v", rs, err)
	}
	s, err := d.n.RelaySettings()
	if err != nil || s == nil || s.Version != 2 {
		t.Fatalf("settings after removing last = %+v %v", s, err)
	}
}

func TestLegacyRelayConfigMigrated(t *testing.T) {
	d := newDevice(t, "笔记本")
	cfg := fakeRelayCfg(7, "o")
	if err := d.n.Store().SetSecretMeta(store.MetaRelayConfig, cfg.ConfigString()); err != nil {
		t.Fatal(err)
	}
	n, err := Open(Options{DataDir: d.dir, Keys: d.keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	s, err := n.RelaySettings()
	if err != nil || s == nil {
		t.Fatalf("settings = %v %v", s, err)
	}
	if s.Version != 0 || len(s.Configs) != 1 || s.Configs[0].ConfigString() != cfg.ConfigString() || !bytes.Equal(s.Signer, n.id.Public) {
		t.Fatalf("migrated = %+v", s)
	}
}

func TestOrphanedRelaySettingsResigned(t *testing.T) {
	a, b := newDevice(t, "A"), newDevice(t, "B")
	a.start(t)
	joinCircle(t, a, b)
	cfg := fakeRelayCfg(3, "t")
	if _, err := b.n.AddRelay(cfg); err != nil {
		t.Fatal(err)
	}
	bs, _ := b.n.RelaySettings()
	if err := a.n.saveRelaySettings(bs); err != nil {
		t.Fatal(err)
	}
	if err := a.n.RemoveDevice(b.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	s, err := a.n.RelaySettings()
	if err != nil || s == nil {
		t.Fatal(s, err)
	}
	if !bytes.Equal(s.Signer, a.n.id.Public) || s.Version != bs.Version || len(s.Configs) != 1 || s.Configs[0].ConfigString() != cfg.ConfigString() {
		t.Fatalf("resigned = %+v (b version %d)", s, bs.Version)
	}
}

// R 签了版本 4，A 随后改成版本 5，C 还拿着 R 的版本 4 没同步。R 被移除后 C 用同样的版本 4 重新签名（不加一），
// 所以不会靠签名者比较盖过 A 的版本 5：同步之后大家都是 A 的版本 5。
func TestOrphanResignKeepsVersion(t *testing.T) {
	a, c, r := newDevice(t, "A"), newDevice(t, "C"), newDevice(t, "R")
	a.start(t)
	joinCircle(t, a, c)
	joinCircle(t, a, r)
	r1, r2 := fakeRelayCfg(1, "a"), fakeRelayCfg(2, "b")
	rs, err := relayset.Sign(r.n.id.Private, 4, []relay.Config{r1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*testDevice{a, c} {
		if err := d.n.saveRelaySettings(rs); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.n.AddRelay(r2); err != nil {
		t.Fatal(err)
	}
	want := mustRelaySettings(t, a)
	if want.Version != 5 {
		t.Fatalf("A's edit = v%d", want.Version)
	}
	if err := a.n.RemoveDevice(r.n.id.KeyID); err != nil && err != ErrRemovedKeyPending {
		t.Fatal(err)
	}
	// C 离线拿到移除记录：重新签名 R 的设置，版本不变。
	ac, _ := a.n.snapshot()
	if _, err := c.n.mergeChain(ac.Records()); err != nil {
		t.Fatal(err)
	}
	cs := mustRelaySettings(t, c)
	if !bytes.Equal(cs.Signer, c.n.id.Public) || cs.Version != 4 || !slices.Equal(relayStrings(c), configStrings(r1)) {
		t.Fatalf("C re-signed v%d %v signed by C=%v, want R's v4 content at v4", cs.Version, relayStrings(c), bytes.Equal(cs.Signer, c.n.id.Public))
	}
	c.start(t)
	setAddr(t, c, a)
	eventually(t, "everyone has A's v5", func() bool {
		return relayRaw(a) == string(want.Raw) && relayRaw(c) == string(want.Raw)
	})
}

// RelayOutdatedPeers 只列在线且没声明 relays 的成员；RelaySettingsInfo 给出签名者名字。
func TestRelayOutdatedPeersAndInfo(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	if _, _, ok := laptop.n.RelaySettingsInfo(); ok {
		t.Fatal("no settings yet")
	}
	if _, err := laptop.n.AddRelay(fakeRelayCfg(1, "a")); err != nil {
		t.Fatal(err)
	}
	if name, at, ok := laptop.n.RelaySettingsInfo(); !ok || name != "笔记本" || at.IsZero() {
		t.Fatalf("info = %q %v %v", name, at, ok)
	}
	id := desktop.n.id.KeyID
	eventually(t, "desktop online", func() bool { return online(t, laptop, id) })
	if err := laptop.n.Store().SetPeerFeatures(id, []string{FeatureRename}); err != nil {
		t.Fatal(err)
	}
	if got := laptop.n.RelayOutdatedPeers(); len(got) != 1 || got[0] != "台式机" {
		t.Fatalf("outdated = %v", got)
	}
	if err := laptop.n.Store().SetPeerFeatures(id, []string{FeatureRename, FeatureRelays}); err != nil {
		t.Fatal(err)
	}
	if got := laptop.n.RelayOutdatedPeers(); len(got) != 0 {
		t.Fatalf("outdated = %v", got)
	}
}

// 带 tcp= 的配置添加两次：第二次是更新，保存下来的配置串与第一次相同（同步按串比较，不能反复出新版本）。
func TestConfigStringTCPStable(t *testing.T) {
	d := newDevice(t, "笔记本")
	if err := d.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	cfg := fakeRelayCfg(1, "a")
	cfg.TCPPort = "443"
	if updated, err := d.n.AddRelay(cfg); err != nil || updated {
		t.Fatalf("first add: updated=%v err=%v", updated, err)
	}
	first, err := d.n.Relays()
	if err != nil || len(first) != 1 {
		t.Fatalf("Relays = %v %v", first, err)
	}
	if updated, err := d.n.AddRelay(cfg); err != nil || !updated {
		t.Fatalf("second add: updated=%v err=%v", updated, err)
	}
	second, err := d.n.Relays()
	if err != nil || len(second) != 1 || second[0].ConfigString() != first[0].ConfigString() {
		t.Fatalf("Relays changed: %v -> %v", first, second)
	}
	if first[0].TCPPort != "443" {
		t.Fatalf("tcp port lost: %+v", first[0])
	}
}

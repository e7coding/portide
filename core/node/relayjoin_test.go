package node

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"portide/core/pairing"
	"portide/core/relay"
	"portide/core/relayset"
)

// signRelays 让 d 以 version 签名保存 configs（模拟别的设备同步过来的较新版本）。
func signRelays(t *testing.T, d *testDevice, version uint64, configs ...relay.Config) *relayset.Settings {
	t.Helper()
	s, err := relayset.Sign(d.n.id.Private, version, configs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.n.saveRelaySettings(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func mustRelaySettings(t *testing.T, d *testDevice) *relayset.Settings {
	t.Helper()
	s, err := d.n.RelaySettings()
	if err != nil || s == nil {
		t.Fatalf("relay settings = %v, %v", s, err)
	}
	return s
}

func configStrings(cs ...relay.Config) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ConfigString())
	}
	return out
}

// 邀请方有 [R1]（版本 3），加入方没有中继：加入方原样采用邀请方的设置。
func TestJoinAdoptsInviterRelays(t *testing.T) {
	r1 := startTestRelay(t)
	inviter, joiner := newDevice(t, "笔记本"), newDevice(t, "台式机")
	if err := inviter.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	want := signRelays(t, inviter, 3, r1)
	inviter.start(t)
	joinCircle(t, inviter, joiner)
	if got := mustRelaySettings(t, joiner); !bytes.Equal(got.Raw, want.Raw) {
		t.Fatalf("joiner settings v%d %v, want the inviter's v3 unchanged", got.Version, relayStrings(joiner))
	}
}

// 加入方单机时有 [R2]，邀请方有 [R1]（版本 3）：加入方合成 [R1, R2]、由自己签名、版本 4；连上后邀请方也采用。
func TestJoinKeepsJoinerRelay(t *testing.T) {
	r1, r2 := startTestRelay(t), startTestRelay(t)
	inviter, joiner := newDevice(t, "笔记本"), newDevice(t, "台式机")
	if err := inviter.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	signRelays(t, inviter, 3, r1)
	useRelay(t, joiner, r2)
	inviter.start(t)
	joinCircle(t, inviter, joiner)
	got := mustRelaySettings(t, joiner)
	if !slices.Equal(relayStrings(joiner), configStrings(r1, r2)) || got.Version != 4 || !bytes.Equal(got.Signer, joiner.n.id.Public) {
		t.Fatalf("joiner settings v%d %v signed by inviter=%v", got.Version, relayStrings(joiner), bytes.Equal(got.Signer, inviter.n.id.Public))
	}
	joiner.start(t)
	waitRelays(t, "inviter adopts [R1, R2]", inviter, r1, r2)
}

// 旧版本邀请方（不声明 relays、不发 relay_settings，只有 relay_config）：加入方得到 [R1]，由自己签名、版本 1
// （收到了东西就离开版本 0，设计"实现时的调整"）。
func TestJoinOldInviterRelayConfig(t *testing.T) {
	r1 := startTestRelay(t)
	inviter, joiner := newDevice(t, "笔记本"), newDevice(t, "台式机")
	inviter.n.features = slices.DeleteFunc(slices.Clone(features), func(f string) bool { return f == FeatureRelays })
	if err := inviter.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	signRelays(t, inviter, 3, r1)
	inviter.start(t)
	joinCircle(t, inviter, joiner)
	got := mustRelaySettings(t, joiner)
	if !slices.Equal(relayStrings(joiner), configStrings(r1)) || got.Version != 1 || !bytes.Equal(got.Signer, joiner.n.id.Public) {
		t.Fatalf("joiner settings v%d %v", got.Version, relayStrings(joiner))
	}
}

// 旧版本邀请方只给 relay_config [R1]，加入方单机时有 [R2]：新圈的中继在前，得到 [R1, R2]、版本 1。
func TestJoinOldInviterRelayFirst(t *testing.T) {
	r1, r2 := fakeRelayCfg(1, "a"), fakeRelayCfg(2, "b")
	inviter, joiner := newDevice(t, "笔记本"), newDevice(t, "台式机")
	inviter.n.features = slices.DeleteFunc(slices.Clone(features), func(f string) bool { return f == FeatureRelays })
	if err := inviter.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	signRelays(t, inviter, 3, r1)
	useRelay(t, joiner, r2)
	inviter.start(t)
	joinCircle(t, inviter, joiner)
	got := mustRelaySettings(t, joiner)
	if !slices.Equal(relayStrings(joiner), configStrings(r1, r2)) || got.Version != 1 || !bytes.Equal(got.Signer, joiner.n.id.Public) {
		t.Fatalf("joiner settings v%d %v, want [R1, R2] v1", got.Version, relayStrings(joiner))
	}
}

// 加入方的列表 [R1, R2] 包含邀请方的 [R1]（版本 3）：加入方保留自己的列表、由自己签名、版本 4；
// 连上后邀请方原样采用加入方的设置。
func TestJoinJoinerSupersetKept(t *testing.T) {
	r1, r2 := fakeRelayCfg(1, "a"), fakeRelayCfg(2, "b")
	inviter, joiner := newDevice(t, "笔记本"), newDevice(t, "台式机")
	if err := inviter.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	signRelays(t, inviter, 3, r1)
	useRelay(t, joiner, r1)
	useRelay(t, joiner, r2)
	inviter.start(t)
	joinCircle(t, inviter, joiner)
	got := mustRelaySettings(t, joiner)
	if !slices.Equal(relayStrings(joiner), configStrings(r1, r2)) || got.Version != 4 || !bytes.Equal(got.Signer, joiner.n.id.Public) {
		t.Fatalf("joiner settings v%d %v", got.Version, relayStrings(joiner))
	}
	joiner.start(t)
	eventually(t, "inviter adopts the joiner's settings unchanged", func() bool { return relayRaw(inviter) == string(got.Raw) })
}

// 反向加入（ADOPT）：电脑和 Windows 的圈有 [R1]（版本 2），单机的手机有 [R2]。电脑用手机的配对码加入，
// 手机换到电脑的圈，把两边合成 [R1, R2]、由自己签名、版本 3；连上后电脑也采用。
func TestAdoptMergesJoinerRelays(t *testing.T) {
	r1, r2 := fakeRelayCfg(1, "a"), fakeRelayCfg(2, "b")
	pc, win, phone := newDevice(t, "电脑"), newDevice(t, "Windows"), newDevice(t, "手机")
	pc.start(t)
	joinCircle(t, pc, win)
	signRelays(t, pc, 2, r1)
	useRelay(t, phone, r2)
	phone.start(t)
	code, _ := invite(t, phone)
	res, err := pc.n.Join(t.Context(), code, []string{phone.n.Addr()}, false)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if res.Mode != pairing.ModeAdopt {
		t.Fatalf("join mode = %v, want adopt", res.Mode)
	}
	eventually(t, "phone switched to pc's circle", func() bool { return phone.n.CircleID() == pc.n.CircleID() })
	got := mustRelaySettings(t, phone)
	if !slices.Equal(relayStrings(phone), configStrings(r1, r2)) || got.Version != 3 || !bytes.Equal(got.Signer, phone.n.id.Public) {
		t.Fatalf("phone settings v%d %v", got.Version, relayStrings(phone))
	}
	eventually(t, "pc adopts the merged settings", func() bool { return relayRaw(pc) == string(got.Raw) })
}

// 并集超过 4 个（邀请方 4 个、加入方 1 个不同的）：配对照常成功，保留邀请方的 4 个，多出来的写日志。
func TestJoinUnionCappedAtFour(t *testing.T) {
	var rs []relay.Config
	for range relayset.MaxRelays + 1 {
		rs = append(rs, startTestRelay(t))
	}
	inviter, joiner := newDevice(t, "笔记本"), newDevice(t, "台式机")
	logs := &logRecorder{}
	joiner.n.opts.Logf = logs.Logf
	if err := inviter.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	signRelays(t, inviter, 1, rs[:relayset.MaxRelays]...)
	useRelay(t, joiner, rs[relayset.MaxRelays])
	inviter.start(t)
	joinCircle(t, inviter, joiner)
	if !slices.Equal(relayStrings(joiner), configStrings(rs[:relayset.MaxRelays]...)) {
		t.Fatalf("joiner relays = %v, want the inviter's four", relayStrings(joiner))
	}
	if !logs.contains("中继超过 4 个，没有加入") {
		t.Fatal("the dropped relay must be logged")
	}
}

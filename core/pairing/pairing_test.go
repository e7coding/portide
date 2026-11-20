package pairing

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"filippo.io/cpace"

	"portide/core/chain"
	"portide/core/identity"
	"portide/core/pb"
	"portide/core/wire"
)

var (
	exporterA    = bytes.Repeat([]byte{0xA}, ExporterLen)
	exporterB    = bytes.Repeat([]byte{0xB}, ExporterLen)
	discoveryKey = bytes.Repeat([]byte{7}, 32)
)

func TestCodeHelpers(t *testing.T) {
	code, err := NewCode()
	if err != nil || len(code) != CodeDigits {
		t.Fatalf("NewCode = %q %v", code, err)
	}
	if got := FormatCode("48213907"); got != "4821-3907" {
		t.Fatalf("FormatCode = %q", got)
	}
	if got, err := NormalizeCode(" 4821-3907 "); err != nil || got != "48213907" {
		t.Fatalf("NormalizeCode = %q %v", got, err)
	}
	for _, bad := range []string{"1234567", "1234-567a", ""} {
		if _, err := NormalizeCode(bad); err == nil {
			t.Fatalf("NormalizeCode(%q) should fail", bad)
		}
	}
}

func mustID(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type served struct {
	res *Result
	err error
}

// pair 在内存管道两端执行一次配对：邀请方用 inviteCode 和 inviterExp，加入方用 joinCode 和 joinerExp。
func pair(t *testing.T, inv Invite, inviterExp []byte, joiner *identity.Identity, joinCode string, joinerExp []byte) (served, *Joined, error) {
	t.Helper()
	return pairWith(t, inv, inviterExp, joiner, joinCode, joinerExp, []string{chain.FeatureRename})
}

// pairWith 同 pair，加入方声明 features（nil 相当于不认识 features 字段的旧版本）。
func pairWith(t *testing.T, inv Invite, inviterExp []byte, joiner *identity.Identity, joinCode string, joinerExp []byte, features []string) (served, *Joined, error) {
	t.Helper()
	return pairReq(t, inv, inviterExp, JoinRequest{Self: joiner, Name: "台式机", Code: joinCode, Features: features}, joinerExp)
}

// pairReq 在内存管道两端执行一次配对，加入方用完整的 JoinRequest。
func pairReq(t *testing.T, inv Invite, inviterExp []byte, req JoinRequest, joinerExp []byte) (served, *Joined, error) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan served, 1)
	go func() {
		res, err := Serve(b, inviterExp, inv)
		b.Close()
		done <- served{res, err}
	}()
	joined, err := Join(a, joinerExp, req)
	a.Close()
	return <-done, joined, err
}

func newInvite(t *testing.T, code string) (Invite, *identity.Identity) {
	t.Helper()
	inviter := mustID(t)
	c, err := chain.New(inviter, "笔记本")
	if err != nil {
		t.Fatal(err)
	}
	return Invite{Code: code, Self: inviter, Chain: c, DiscoveryKey: discoveryKey}, inviter
}

func TestPairingSuccess(t *testing.T) {
	inv, inviter := newInvite(t, "48213907")
	inv.RelayConfig = "portide-relay://relay.example.com:7443?fp=00&token=00"
	var committed [][]byte
	inv.Commit = func(r [][]byte) error { committed = r; return nil }
	joiner := mustID(t)
	s, joined, err := pair(t, inv, exporterA, joiner, "48213907", exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("join err = %v, serve err = %v", err, s.err)
	}
	if s.res.JoinerKeyID != joiner.KeyID || s.res.JoinerName != "台式机" || !inv.Chain.IsMember(joiner.KeyID) {
		t.Fatalf("inviter result = %+v", s.res)
	}
	if joined.InviterKeyID != inviter.KeyID || joined.Chain.CircleID() != inv.Chain.CircleID() || !joined.Chain.IsMember(joiner.KeyID) {
		t.Fatal("joiner got a different circle or inviter")
	}
	if !bytes.Equal(joined.DiscoveryKey, discoveryKey) || len(committed) != 2 {
		t.Fatalf("discovery key or commit wrong (committed %d records)", len(committed))
	}
	if joined.RelayConfig != inv.RelayConfig {
		t.Fatalf("relay config = %q", joined.RelayConfig)
	}
}

func TestPairingWrongCode(t *testing.T) {
	inv, _ := newInvite(t, "48213907")
	s, _, err := pair(t, inv, exporterA, mustID(t), "11111111", exporterA)
	if !errors.Is(err, ErrWrongCode) || !errors.Is(s.err, ErrWrongCode) {
		t.Fatalf("join err = %v, serve err = %v; want ErrWrongCode on both", err, s.err)
	}
	if inv.Chain.Len() != 1 {
		t.Fatal("a failed pairing must not change the chain")
	}
}

// 中间人终止两条 TLS 连接，两边导出值不同：即使配对码相同，配对也必须失败。
func TestMITMDifferentExportersFails(t *testing.T) {
	inv, _ := newInvite(t, "48213907")
	s, _, err := pair(t, inv, exporterA, mustID(t), "48213907", exporterB)
	if !errors.Is(err, ErrWrongCode) || !errors.Is(s.err, ErrWrongCode) {
		t.Fatalf("join err = %v, serve err = %v; want ErrWrongCode on both", err, s.err)
	}
	if inv.Chain.Len() != 1 {
		t.Fatal("a failed pairing must not change the chain")
	}
}

func TestCommitFailureLeavesChainUntouched(t *testing.T) {
	inv, _ := newInvite(t, "48213907")
	inv.Commit = func([][]byte) error { return errors.New("disk full") }
	joiner := mustID(t)
	s, _, err := pair(t, inv, exporterA, joiner, "48213907", exporterA)
	if s.err == nil || err == nil {
		t.Fatalf("both sides must fail when Commit fails: serve %v, join %v", s.err, err)
	}
	if inv.Chain.Len() != 1 || inv.Chain.IsMember(joiner.KeyID) {
		t.Fatal("a failed commit must leave the chain unchanged")
	}
}

// 上一次配对已在邀请方提交、但加入方没收到欢迎消息：用同一身份重新配对必须成功，且不再追加记录。
func TestRepairAfterLostWelcome(t *testing.T) {
	inv, _ := newInvite(t, "48213907")
	joiner := mustID(t)
	if err := inv.Chain.AddDevice(inv.Self, joiner.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	commits := 0
	inv.Commit = func([][]byte) error { commits++; return nil }
	s, joined, err := pair(t, inv, exporterA, joiner, "48213907", exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("re-pairing an existing member must succeed: join %v, serve %v", err, s.err)
	}
	if joined.Chain.Len() != 2 || inv.Chain.Len() != 2 || commits != 0 {
		t.Fatalf("re-pair must not append records: joined=%d inviter=%d commits=%d", joined.Chain.Len(), inv.Chain.Len(), commits)
	}
}

// 知道配对码、但拿不出设备密钥签名的加入方（签名绑定的是另一条连接）不能入圈。
func TestJoinerMustProveDeviceKey(t *testing.T) {
	inv, _ := newInvite(t, "48213907")
	a, b := net.Pipe()
	done := make(chan served, 1)
	go func() {
		res, err := Serve(b, exporterA, inv)
		b.Close()
		done <- served{res, err}
	}()
	victim := mustID(t)
	msgA, st, _ := cpace.Start("48213907", contextInfo(exporterA))
	_ = wire.Write(a, &pb.PairHello{CpaceMsgA: msgA})
	var ch pb.PairChallenge
	_ = wire.Read(a, &ch)
	key, _ := st.Finish(ch.CpaceMsgB)
	tag, _ := confirmTag(key, roleInitiator)
	_ = wire.Write(a, &pb.PairConfirm{
		InitiatorTag: tag, DeviceKey: victim.Public, DeviceName: "冒充",
		Signature: ed25519.Sign(victim.Private, signed(joinerContext, exporterB)), // 绑定的是另一条连接
	})
	var w pb.PairWelcome
	_ = wire.Read(a, &w)
	_, _ = io.Copy(io.Discard, a)
	a.Close()
	s := <-done
	if s.err == nil || w.Error == "" || len(w.Chain) != 0 {
		t.Fatalf("forged identity must be refused: serve err %v, welcome %+v", s.err, &w)
	}
	if inv.Chain.IsMember(victim.KeyID) {
		t.Fatal("the chain must not contain a device that did not prove its key")
	}
}

func TestConfirmTagsAreDirectional(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	a, err := confirmTag(key, roleInitiator)
	if err != nil {
		t.Fatal(err)
	}
	b, err := confirmTag(key, roleResponder)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("initiator and responder tags must differ")
	}
}

// 对方在证明知道配对码之前就发一个声称很大的长度头：邀请方必须立刻拒绝，不等待消息体，也不发出任何内容。
func TestOversizedPairHelloRejected(t *testing.T) {
	inv, _ := newInvite(t, "48213907")
	a, b := net.Pipe()
	defer a.Close()
	done := make(chan error, 1)
	go func() {
		_, err := Serve(b, exporterA, inv)
		b.Close()
		done <- err
	}()
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 1<<20)
	if _, err := a.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(a)
	if len(got) != 0 {
		t.Fatalf("inviter sent %d bytes to an oversized hello", len(got))
	}
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("serve err = %v, want an exceeds-limit error", err)
	}
}

// 链上已有改名记录时，没声明支持改名的加入方（旧版本）读不了这条链：邀请方在提交之前拒绝，不留下连不上的成员。
func TestOutdatedJoinerRefusedWhenChainHasRenames(t *testing.T) {
	inv, inviter := newInvite(t, "12345678")
	commits := 0
	inv.Commit = func([][]byte) error { commits++; return nil }

	// 还没有改名记录：旧版本照样能加入。
	if s, _, err := pairWith(t, inv, exporterA, mustID(t), "12345678", exporterA, nil); err != nil || s.err != nil {
		t.Fatalf("no renames yet: join=%v serve=%v", err, s.err)
	}
	if err := inv.Chain.RenameDevice(inviter, inviter.KeyID, "joy 的笔记本"); err != nil {
		t.Fatal(err)
	}
	before, commitsBefore := inv.Chain.Len(), commits
	s, _, err := pairWith(t, inv, exporterA, mustID(t), "12345678", exporterA, nil)
	if !errors.Is(s.err, ErrJoinerOutdated) || err == nil || !strings.Contains(err.Error(), "update") {
		t.Fatalf("outdated joiner: serve=%v join=%v", s.err, err)
	}
	if inv.Chain.Len() != before || commits != commitsBefore {
		t.Fatal("a refused joiner must not be added to the chain")
	}
	if s, _, err := pairWith(t, inv, exporterA, mustID(t), "12345678", exporterA, []string{"other", chain.FeatureRename}); err != nil || s.err != nil {
		t.Fatalf("an up-to-date joiner: join=%v serve=%v", err, s.err)
	}
}

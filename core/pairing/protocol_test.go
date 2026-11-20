package pairing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/cpace"

	"portide/core/chain"
	"portide/core/identity"
	"portide/core/pb"
	"portide/core/relay"
	"portide/core/relayset"
	"portide/core/wire"
)

const testCode = "48213907"

var (
	newFeatures = []string{chain.FeatureRename, FeatureCircle2}
	joinerDKey  = bytes.Repeat([]byte{9}, 32)
)

// circleOf 建一个以 self 为创建者、共 members 台设备的圈（其余设备随机生成）。
func circleOf(t *testing.T, self *identity.Identity, name string, members int) *chain.Chain {
	t.Helper()
	c, err := chain.New(self, name)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < members; i++ {
		if err := c.AddDevice(self, mustID(t).Public, "其他"+string(rune('A'+i))); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// circleInvite 返回声明了 circle2、圈里有 members 台设备的邀请方。
func circleInvite(t *testing.T, members int) Invite {
	t.Helper()
	inviter := mustID(t)
	return Invite{
		Code: testCode, Self: inviter, Chain: circleOf(t, inviter, "笔记本", members),
		DiscoveryKey: discoveryKey, Features: newFeatures,
	}
}

// circleJoiner 返回声明了 circle2 的加入方请求；members 为 0 表示不在圈里。
func circleJoiner(t *testing.T, members int) JoinRequest {
	t.Helper()
	joiner := mustID(t)
	req := JoinRequest{Self: joiner, Name: "台式机", Code: testCode, Features: newFeatures, DiscoveryKey: joinerDKey, RelayConfig: "portide-relay://joiner"}
	if members > 0 {
		req.Chain = circleOf(t, joiner, "台式机", members)
	}
	return req
}

func TestJoinSoloJoinerJoinsInviter(t *testing.T) {
	inv := circleInvite(t, 2)
	req := circleJoiner(t, 1)
	s, joined, err := pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("join err = %v, serve err = %v", err, s.err)
	}
	if joined.Mode != ModeJoin || s.res.Mode != ModeJoin || joined.InviterName != "笔记本" {
		t.Fatalf("joined mode %q name %q, result mode %q", joined.Mode, joined.InviterName, s.res.Mode)
	}
	if !joined.Chain.IsMember(req.Self.KeyID) || joined.Chain.CircleID() != inv.Chain.CircleID() || joined.Chain.Len() != 3 {
		t.Fatal("joiner must get the inviter's circle with itself added")
	}
	if !bytes.Equal(joined.DiscoveryKey, discoveryKey) {
		t.Fatal("joiner must get the inviter's discovery key")
	}
}

func TestBothSoloJoinerJoinsInviter(t *testing.T) {
	inv := circleInvite(t, 1)
	req := circleJoiner(t, 1)
	s, joined, err := pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("join err = %v, serve err = %v", err, s.err)
	}
	if joined.Mode != ModeJoin || s.res.Mode != ModeJoin {
		t.Fatalf("mode = %q / %q, want join", joined.Mode, s.res.Mode)
	}
	if joined.Chain.CircleID() != inv.Chain.CircleID() || !joined.Chain.IsMember(req.Self.KeyID) || !joined.Chain.IsMember(inv.Self.KeyID) {
		t.Fatal("both solo: the joiner must join the inviter's circle")
	}
}

func TestAdoptMultiJoinerTakesSoloInviter(t *testing.T) {
	inv := circleInvite(t, 1)
	var adopted *Adopted
	inv.Adopt = func(a *Adopted) error { adopted = a; return nil }
	inviterCommits := 0
	inv.Commit = func([][]byte) error { inviterCommits++; return nil }
	req := circleJoiner(t, 3)
	var committed [][][]byte
	req.Commit = func(r [][]byte) error { committed = append(committed, r); return nil }
	before := req.Chain.Len()

	s, joined, err := pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("join err = %v, serve err = %v", err, s.err)
	}
	if len(committed) != 1 || len(committed[0]) != before+1 {
		t.Fatalf("joiner must commit once with one more record: %d commits", len(committed))
	}
	if adopted == nil || !adopted.Chain.IsMember(inv.Self.KeyID) || !adopted.Chain.IsMember(req.Self.KeyID) {
		t.Fatal("Adopt must receive a chain with both devices")
	}
	if adopted.Chain.CircleID() != req.Chain.CircleID() || !bytes.Equal(adopted.DiscoveryKey, joinerDKey) ||
		adopted.RelayConfig != req.RelayConfig || adopted.JoinerKeyID != req.Self.KeyID {
		t.Fatalf("Adopt got the wrong circle state: %+v", adopted)
	}
	if s.res.Mode != ModeAdopt || joined.Mode != ModeAdopt || s.res.JoinerKeyID != req.Self.KeyID {
		t.Fatalf("mode = %q / %q, want adopt", s.res.Mode, joined.Mode)
	}
	if joined.Chain.Len() != before+1 || !bytes.Equal(joined.DiscoveryKey, joinerDKey) || joined.RelayConfig != req.RelayConfig {
		t.Fatal("joiner result must carry its own (extended) circle")
	}
	if joined.InviterKeyID != inv.Self.KeyID || joined.InviterName != "笔记本" {
		t.Fatalf("inviter = %q %q", joined.InviterKeyID, joined.InviterName)
	}
	if inv.Chain.Len() != 1 || inviterCommits != 0 || req.Chain.Len() != before {
		t.Fatal("Serve and Join must not change the callers' chains on adopt")
	}
}

// 加入方提交了"加入 邀请方"、但邀请方换圈失败（PairAck 没送到或报错）：再配一次时加入方不再追加，直接重发链。
func TestAdoptRepairAfterLostAck(t *testing.T) {
	inv := circleInvite(t, 1)
	var adopted []*Adopted
	inv.Adopt = func(a *Adopted) error {
		adopted = append(adopted, a)
		if len(adopted) == 1 {
			return errors.New("disk full")
		}
		return nil
	}
	req := circleJoiner(t, 3)
	var committed [][][]byte
	req.Commit = func(r [][]byte) error { committed = append(committed, r); return nil }

	s, _, err := pairReq(t, inv, exporterA, req, exporterA)
	if err == nil || s.err == nil || !strings.Contains(err.Error(), "could not switch") {
		t.Fatalf("first pairing must fail on both sides: join %v, serve %v", err, s.err)
	}
	if len(committed) != 1 {
		t.Fatalf("joiner commits = %d", len(committed))
	}
	// 加入方已经持久化了含邀请方的链。
	c, err := chain.Load(committed[0])
	if err != nil {
		t.Fatal(err)
	}
	req.Chain = c
	s, joined, err := pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("re-pairing must succeed: join %v, serve %v", err, s.err)
	}
	if len(committed) != 1 {
		t.Fatal("re-pairing must not append again")
	}
	if len(adopted) != 2 || !slices.EqualFunc(adopted[1].Chain.Records(), committed[0], bytes.Equal) {
		t.Fatal("Adopt must get the chain committed the first time")
	}
	if joined.Mode != ModeAdopt || joined.Chain.Len() != len(committed[0]) {
		t.Fatalf("joined mode %q len %d", joined.Mode, joined.Chain.Len())
	}
}

func testBothMulti(t *testing.T, mergeOK bool, want error) {
	t.Helper()
	inv := circleInvite(t, 2)
	commits := 0
	inv.Commit = func([][]byte) error { commits++; return nil }
	inv.Adopt = func(*Adopted) error { commits++; return nil }
	req := circleJoiner(t, 2)
	req.MergeOK = mergeOK
	req.Commit = func([][]byte) error { commits++; return nil }
	s, _, err := pairReq(t, inv, exporterA, req, exporterA)
	if !errors.Is(err, want) || !errors.Is(s.err, want) {
		t.Fatalf("join err = %v, serve err = %v; want %v", err, s.err, want)
	}
	if inv.Chain.Len() != 2 || req.Chain.Len() != 2 || commits != 0 {
		t.Fatal("a refused merge must not change either chain")
	}
}

func TestBothMultiMergeUnsupported(t *testing.T) { testBothMulti(t, true, ErrMergeUnsupported) }

func TestBothMultiMergeNotConfirmed(t *testing.T) { testBothMulti(t, false, ErrMergeNotConfirmed) }

func TestSameCircleRefused(t *testing.T) {
	inv := circleInvite(t, 1)
	req := circleJoiner(t, 0)
	if err := inv.Chain.AddDevice(inv.Self, req.Self.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	c, err := chain.Load(inv.Chain.Records())
	if err != nil {
		t.Fatal(err)
	}
	req.Chain = c
	calls := 0
	inv.Commit = func([][]byte) error { calls++; return nil }
	inv.Adopt = func(*Adopted) error { calls++; return nil }
	req.Commit = func([][]byte) error { calls++; return nil }
	inviterBefore, joinerBefore := inv.Chain.Records(), req.Chain.Records()
	s, _, err := pairReq(t, inv, exporterA, req, exporterA)
	if !errors.Is(err, ErrSameCircle) || !errors.Is(s.err, ErrSameCircle) {
		t.Fatalf("join err = %v, serve err = %v; want ErrSameCircle", err, s.err)
	}
	if calls != 0 || !slices.EqualFunc(inv.Chain.Records(), inviterBefore, bytes.Equal) ||
		!slices.EqualFunc(req.Chain.Records(), joinerBefore, bytes.Equal) {
		t.Fatal("a same-circle refusal must not commit, adopt or change either chain")
	}
}

// 上次在邀请方提交了、加入方没收到欢迎消息，它的链还是自己的单机圈：照样按 JOIN 重发，不再追加。
func TestLostWelcomeRepairStillJoins(t *testing.T) {
	inv := circleInvite(t, 2)
	req := circleJoiner(t, 1)
	if err := inv.Chain.AddDevice(inv.Self, req.Self.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	commits := 0
	inv.Commit = func([][]byte) error { commits++; return nil }
	s, joined, err := pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("join err = %v, serve err = %v", err, s.err)
	}
	if joined.Mode != ModeJoin || inv.Chain.Len() != 3 || commits != 0 || joined.Chain.CircleID() != inv.Chain.CircleID() {
		t.Fatalf("re-pair must resend the chain unchanged: mode %q len %d commits %d", joined.Mode, inv.Chain.Len(), commits)
	}
}

// oldInviter 模拟 circle2 之前的邀请方：PairChallenge 不带 features，读 PairConfirm 后直接回 PairWelcome。
// 返回读 PairConfirm 的错误（加入方没发就是流结束）。
func oldInviter(rw io.ReadWriter, exporter []byte, self *identity.Identity, c *chain.Chain) error {
	var hello pb.PairHello
	if err := wire.Read(rw, &hello); err != nil {
		return err
	}
	msgB, key, err := cpace.Exchange(testCode, contextInfo(exporter), hello.CpaceMsgA)
	if err != nil {
		return err
	}
	tag, _ := confirmTag(key, roleResponder)
	if err := wire.Write(rw, &pb.PairChallenge{CpaceMsgB: msgB, ResponderTag: tag}); err != nil {
		return err
	}
	var confirm pb.PairConfirm
	if err := wire.Read(rw, &confirm); err != nil {
		return err
	}
	if err := c.AddDevice(self, ed25519.PublicKey(confirm.DeviceKey), confirm.DeviceName); err != nil {
		return err
	}
	return wire.Write(rw, &pb.PairWelcome{
		Chain: c.Records(), DiscoveryKey: discoveryKey, InviterKey: self.Public,
		Signature: ed25519.Sign(self.Private, signed(inviterContext, exporter)),
	})
}

func pairOldInviter(t *testing.T, req JoinRequest) (error, *chain.Chain, *Joined, error) {
	t.Helper()
	inviter := mustID(t)
	c := circleOf(t, inviter, "笔记本", 2)
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() {
		err := oldInviter(b, exporterA, inviter, c)
		_, _ = io.Copy(io.Discard, b)
		b.Close()
		done <- err
	}()
	joined, err := Join(a, exporterA, req)
	a.Close()
	return <-done, c, joined, err
}

func TestJoinOldInviterSoloJoiner(t *testing.T) {
	req := circleJoiner(t, 1)
	serr, c, joined, err := pairOldInviter(t, req)
	if err != nil || serr != nil {
		t.Fatalf("join err = %v, old inviter err = %v", err, serr)
	}
	if joined.Mode != ModeJoin || joined.Chain.CircleID() != c.CircleID() || !joined.Chain.IsMember(req.Self.KeyID) {
		t.Fatal("a solo joiner must join an old inviter's circle")
	}
	if joined.InviterName != "笔记本" {
		t.Fatalf("inviter name = %q", joined.InviterName)
	}
}

func TestJoinOldInviterMultiJoinerStops(t *testing.T) {
	req := circleJoiner(t, 2)
	serr, c, _, err := pairOldInviter(t, req)
	if !errors.Is(err, ErrInviterOutdated) {
		t.Fatalf("join err = %v, want ErrInviterOutdated", err)
	}
	if !errors.Is(serr, io.EOF) || c.Len() != 2 {
		t.Fatalf("joiner must stop before PairConfirm: old inviter err = %v", serr)
	}
}

// 旧版本加入方：只声明 rename、不发 circle，读完 PairChallenge、发 PairConfirm 后直接读 PairWelcome。
func TestOldJoinerGetsPlainWelcome(t *testing.T) {
	inv := circleInvite(t, 2)
	a, b := net.Pipe()
	done := make(chan served, 1)
	go func() {
		res, err := Serve(b, exporterA, inv)
		b.Close()
		done <- served{res, err}
	}()
	joiner := mustID(t)
	msgA, st, _ := cpace.Start(testCode, contextInfo(exporterA))
	_ = wire.Write(a, &pb.PairHello{CpaceMsgA: msgA})
	var ch pb.PairChallenge
	if err := wire.Read(a, &ch); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ch.Features, FeatureCircle2) {
		t.Fatalf("challenge features = %v", ch.Features)
	}
	key, _ := st.Finish(ch.CpaceMsgB)
	if want, _ := confirmTag(key, roleResponder); !hmac.Equal(ch.ResponderTag, want) {
		t.Fatal("responder tag mismatch")
	}
	tag, _ := confirmTag(key, roleInitiator)
	_ = wire.Write(a, &pb.PairConfirm{
		InitiatorTag: tag, DeviceKey: joiner.Public, DeviceName: "旧台式机",
		Signature: ed25519.Sign(joiner.Private, signed(joinerContext, exporterA)),
		Features:  []string{chain.FeatureRename},
	})
	var w pb.PairWelcome
	if err := wire.Read(a, &w); err != nil {
		t.Fatal(err)
	}
	a.Close()
	s := <-done
	if s.err != nil || s.res.Mode != ModeJoin {
		t.Fatalf("serve err = %v, result %+v", s.err, s.res)
	}
	if w.Error != "" || !bytes.Equal(w.InviterKey, inv.Self.Public) ||
		!ed25519.Verify(ed25519.PublicKey(w.InviterKey), signed(inviterContext, exporterA), w.Signature) {
		t.Fatalf("old joiner must get a plain, signed welcome: %+v", &w)
	}
	c, err := chain.Load(w.Chain)
	if err != nil || !c.IsMember(joiner.KeyID) || !inv.Chain.IsMember(joiner.KeyID) {
		t.Fatalf("welcome chain must contain the old joiner: %v", err)
	}
}

// staleCopy 返回 c 此刻的副本（模拟一台离线设备手里的旧链）。
func staleCopy(t *testing.T, c *chain.Chain) *chain.Chain {
	t.Helper()
	cp, err := chain.Load(c.Records())
	if err != nil {
		t.Fatal(err)
	}
	return cp
}

// 设计方案 6.3 第 2 步：创世记录相同就是同一个圈。邀请方 Y 的圈原来是 {Y, X, L}，用户移除了 X 和 L，Y 单机；
// 离线的 X 还拿着旧链 {Y, X, L}，输入 Y 的配对码：必须按 JOIN 把 X 重新加进 Y 现在的链，不能 ADOPT 用旧链覆盖 Y。
func TestStaleSameCircleCopyRejoins(t *testing.T) {
	inv := circleInvite(t, 1)
	x, l := mustID(t), mustID(t)
	for _, d := range []*identity.Identity{x, l} {
		if err := inv.Chain.AddDevice(inv.Self, d.Public, "设备"); err != nil {
			t.Fatal(err)
		}
	}
	stale := staleCopy(t, inv.Chain)
	for _, d := range []*identity.Identity{x, l} {
		if err := inv.Chain.RemoveDevice(inv.Self, d.KeyID, bytes.Repeat([]byte{3}, 32)); err != nil {
			t.Fatal(err)
		}
	}
	afterRemoval := inv.Chain.Len()
	adopts := 0
	inv.Adopt = func(*Adopted) error { adopts++; return nil }
	req := JoinRequest{Self: x, Name: "台式机", Code: testCode, Features: newFeatures, Chain: stale, DiscoveryKey: joinerDKey}
	joinerCommits := 0
	req.Commit = func([][]byte) error { joinerCommits++; return nil }

	s, joined, err := pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("join err = %v, serve err = %v", err, s.err)
	}
	if s.res.Mode != ModeJoin || joined.Mode != ModeJoin || adopts != 0 || joinerCommits != 0 {
		t.Fatalf("mode = %q / %q, adopts %d, joiner commits %d; want join only", s.res.Mode, joined.Mode, adopts, joinerCommits)
	}
	if inv.Chain.Len() != afterRemoval+1 || !inv.Chain.IsMember(x.KeyID) || inv.Chain.IsMember(l.KeyID) {
		t.Fatal("the inviter's chain must keep its removals and re-add only the joiner")
	}
	if !slices.EqualFunc(joined.Chain.Records(), inv.Chain.Records(), bytes.Equal) || !bytes.Equal(joined.DiscoveryKey, discoveryKey) {
		t.Fatal("the joiner must get the inviter's current chain and key")
	}
}

// 防御：即使 decide 被骗选了 ADOPT（加入方谎报 circle_id），serveAdopt 也不接受和本机创世记录相同的链。
func TestServeAdoptRefusesSameGenesis(t *testing.T) {
	inv := circleInvite(t, 1)
	x := mustID(t)
	c := staleCopy(t, inv.Chain)
	if err := c.AddDevice(inv.Self, x.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	adopts := 0
	inv.Adopt = func(*Adopted) error { adopts++; return nil }
	ack, err := adoptWith(t, inv, x.Public, &pb.PairWelcome{
		Chain: c.Records(), DiscoveryKey: joinerDKey, InviterKey: x.Public,
		Signature: ed25519.Sign(x.Private, signed(adopterContext, exporterA)),
	})
	if err == nil || ack.Error != "same circle" || adopts != 0 {
		t.Fatalf("serveAdopt err = %v, ack %q, adopts %d; want refused as same circle", err, ack.Error, adopts)
	}
}

// 反向欢迎消息里的公钥必须是 PairConfirm 里验证过的那把。
func TestServeAdoptRefusesOtherKey(t *testing.T) {
	inv := circleInvite(t, 1)
	x, other := mustID(t), mustID(t)
	c := circleOf(t, other, "其他", 2)
	if err := c.AddDevice(other, inv.Self.Public, "笔记本"); err != nil {
		t.Fatal(err)
	}
	adopts := 0
	inv.Adopt = func(*Adopted) error { adopts++; return nil }
	ack, err := adoptWith(t, inv, x.Public, &pb.PairWelcome{
		Chain: c.Records(), DiscoveryKey: joinerDKey, InviterKey: other.Public,
		Signature: ed25519.Sign(other.Private, signed(adopterContext, exporterA)),
	})
	if err == nil || ack.Error != "invalid identity" || adopts != 0 {
		t.Fatalf("serveAdopt err = %v, ack %q, adopts %d; want invalid identity", err, ack.Error, adopts)
	}
}

// 反向欢迎消息要用 adopter 上下文签名：拿正向（inviter 上下文）的签名冒充不行。
func TestServeAdoptRefusesInviterContext(t *testing.T) {
	inv := circleInvite(t, 1)
	req := circleJoiner(t, 2)
	if err := req.Chain.AddDevice(req.Self, inv.Self.Public, "笔记本"); err != nil {
		t.Fatal(err)
	}
	adopts := 0
	inv.Adopt = func(*Adopted) error { adopts++; return nil }
	ack, err := adoptWith(t, inv, req.Self.Public, &pb.PairWelcome{
		Chain: req.Chain.Records(), DiscoveryKey: joinerDKey, InviterKey: req.Self.Public,
		Signature: ed25519.Sign(req.Self.Private, signed(inviterContext, exporterA)),
	})
	if err == nil || ack.Error != "invalid identity" || adopts != 0 {
		t.Fatalf("serveAdopt err = %v, ack %q, adopts %d; want invalid identity", err, ack.Error, adopts)
	}
}

// adoptWith 在内存管道上直接运行 serveAdopt：加入方一端写 w，读回 PairAck。
func adoptWith(t *testing.T, inv Invite, joinerPub ed25519.PublicKey, w *pb.PairWelcome) (*pb.PairAck, error) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() {
		err := serveAdopt(b, exporterA, inv, joinerPub)
		b.Close()
		done <- err
	}()
	if err := wire.Write(a, w); err != nil {
		t.Fatal(err)
	}
	var ack pb.PairAck
	_ = wire.Read(a, &ack)
	a.Close()
	return &ack, <-done
}

// 欢迎消息丢了之后 X 已是 B 的成员，但 X 这期间又有了别的设备：不能再按 JOIN 让 X 离开自己的圈（设计方案 1）。
func TestMemberMultiJoinerNotJoined(t *testing.T) {
	for _, mergeOK := range []bool{false, true} {
		inv := circleInvite(t, 2)
		req := circleJoiner(t, 2)
		req.MergeOK = mergeOK
		if err := inv.Chain.AddDevice(inv.Self, req.Self.Public, "台式机"); err != nil {
			t.Fatal(err)
		}
		commits := 0
		inv.Commit = func([][]byte) error { commits++; return nil }
		inv.Adopt = func(*Adopted) error { commits++; return nil }
		req.Commit = func([][]byte) error { commits++; return nil }
		want := ErrMergeNotConfirmed
		if mergeOK {
			want = ErrMergeUnsupported
		}
		s, _, err := pairReq(t, inv, exporterA, req, exporterA)
		if !errors.Is(err, want) || !errors.Is(s.err, want) {
			t.Fatalf("mergeOK %v: join err = %v, serve err = %v; want %v", mergeOK, err, s.err, want)
		}
		if inv.Chain.Len() != 3 || req.Chain.Len() != 2 || commits != 0 {
			t.Fatal("a refused re-pair must not change either chain")
		}
	}
}

// fakeInviter 模拟一个声明了 circle2 的邀请方（可能是恶意的）：读 PairConfirm 后发 d，d 不是拒绝时再发 w（w 可为 nil）；
// d 是 MERGE 时先读加入方的 PairMerge。
func fakeInviter(rw io.ReadWriter, exporter []byte, d *pb.PairDecision, w *pb.PairWelcome) error {
	var hello pb.PairHello
	if err := wire.Read(rw, &hello); err != nil {
		return err
	}
	msgB, key, err := cpace.Exchange(testCode, contextInfo(exporter), hello.CpaceMsgA)
	if err != nil {
		return err
	}
	tag, _ := confirmTag(key, roleResponder)
	if err := wire.Write(rw, &pb.PairChallenge{CpaceMsgB: msgB, ResponderTag: tag, Features: newFeatures}); err != nil {
		return err
	}
	var confirm pb.PairConfirm
	if err := wire.Read(rw, &confirm); err != nil {
		return err
	}
	if err := wire.Write(rw, d); err != nil {
		return err
	}
	if d.Error == "" && d.Mode == pb.PairDecision_MERGE {
		var m pb.PairMerge // 合并时加入方先发来源圈的链
		if err := wire.Read(rw, &m); err != nil {
			return err
		}
	}
	if d.Error == "" && w != nil {
		return wire.Write(rw, w)
	}
	return nil
}

func pairFake(t *testing.T, req JoinRequest, d *pb.PairDecision, w *pb.PairWelcome) (*Joined, error) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() {
		err := fakeInviter(b, exporterA, d, w)
		_, _ = io.Copy(io.Discard, b)
		b.Close()
		done <- err
	}()
	joined, err := Join(a, exporterA, req)
	a.Close()
	if serr := <-done; serr != nil {
		t.Fatalf("fake inviter: %v", serr)
	}
	return joined, err
}

// 本机圈里有其他设备，邀请方却回 JOIN、给的是另一个圈：拒绝，不离开本机的圈。
func TestJoinerRefusesUnexpectedJoin(t *testing.T) {
	inviter := mustID(t)
	req := circleJoiner(t, 2)
	commits := 0
	req.Commit = func([][]byte) error { commits++; return nil }
	c := circleOf(t, inviter, "笔记本", 2)
	if err := c.AddDevice(inviter, req.Self.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(inviter.Private, signed(inviterContext, exporterA))
	d := &pb.PairDecision{InviterKey: inviter.Public, InviterName: "笔记本", Signature: sig}
	w := &pb.PairWelcome{Chain: c.Records(), DiscoveryKey: discoveryKey, InviterKey: inviter.Public, Signature: sig}
	joined, err := pairFake(t, req, d, w)
	if !errors.Is(err, ErrUnexpectedJoin) || joined != nil || commits != 0 || req.Chain.Len() != 2 {
		t.Fatalf("join = %+v, %v; want ErrUnexpectedJoin and nothing committed", joined, err)
	}
}

// PairDecision 的签名不对：加入方拒绝，不读欢迎消息、不提交。
func TestJoinerRefusesBadDecisionSignature(t *testing.T) {
	inviter := mustID(t)
	req := circleJoiner(t, 1)
	d := &pb.PairDecision{
		InviterKey: inviter.Public, InviterName: "笔记本",
		Signature: ed25519.Sign(inviter.Private, signed(adopterContext, exporterA)), // 上下文不对
	}
	joined, err := pairFake(t, req, d, nil)
	if err == nil || joined != nil || !strings.Contains(err.Error(), "identity proof is invalid") {
		t.Fatalf("join = %+v, %v; want an invalid identity proof", joined, err)
	}
}

// 本机的链里已经没有本机（被移除了）：报 0 台设备，按 JOIN 加入对方，不会 ADOPT 失败在"invalid device"。
func TestRemovedJoinerReportsNoMembers(t *testing.T) {
	inv := circleInvite(t, 1)
	inv.Adopt = func(*Adopted) error { return errors.New("must not adopt") }
	req := circleJoiner(t, 0)
	other := mustID(t)
	c := circleOf(t, other, "其他", 3)
	if err := c.AddDevice(other, req.Self.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveDevice(other, req.Self.KeyID, bytes.Repeat([]byte{4}, 32)); err != nil {
		t.Fatal(err)
	}
	req.Chain = c
	s, joined, err := pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("join err = %v, serve err = %v", err, s.err)
	}
	if joined.Mode != ModeJoin || joined.Chain.CircleID() != inv.Chain.CircleID() {
		t.Fatalf("mode = %q; want join into the inviter's circle", joined.Mode)
	}
}

// staleInviterCircle 搭出"邀请方的链比加入方旧"的同一个圈：圈 {Y, L}，Y 离线期间 L 加了 X、X 又移除了 L。
// 返回 Y（邀请方，链 {Y, +L}）和 X（加入方，链 {Y, +L, L 加 X, X 移除 L}），以及共同的前缀 {Y, +L}。
func staleInviterCircle(t *testing.T) (inv Invite, req JoinRequest, base *chain.Chain) {
	t.Helper()
	inv = circleInvite(t, 1)
	l, x := mustID(t), mustID(t)
	if err := inv.Chain.AddDevice(inv.Self, l.Public, "L"); err != nil {
		t.Fatal(err)
	}
	base = staleCopy(t, inv.Chain)
	xc := staleCopy(t, inv.Chain)
	if err := xc.AddDevice(l, x.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	if err := xc.RemoveDevice(x, l.KeyID, bytes.Repeat([]byte{5}, 32)); err != nil {
		t.Fatal(err)
	}
	req = JoinRequest{Self: x, Name: "台式机", Code: testCode, Features: newFeatures, Chain: xc, DiscoveryKey: joinerDKey}
	return inv, req, base
}

// 邀请方 Y 的链比加入方旧（没见过 X，也没见过 L 被移除）：同一个圈、Y 没把 X 记为已移除，拒绝为同一个圈，
// 不能 JOIN 把 X 的新链换回 Y 的旧链（L 会回来）。
func TestStaleInviterSameCircleRefused(t *testing.T) {
	inv, req, _ := staleInviterCircle(t)
	calls := 0
	inv.Commit = func([][]byte) error { calls++; return nil }
	inv.Adopt = func(*Adopted) error { calls++; return nil }
	req.Commit = func([][]byte) error { calls++; return nil }
	inviterBefore, joinerBefore := inv.Chain.Records(), req.Chain.Records()
	s, joined, err := pairReq(t, inv, exporterA, req, exporterA)
	if !errors.Is(err, ErrSameCircle) || !errors.Is(s.err, ErrSameCircle) || joined != nil {
		t.Fatalf("join err = %v, serve err = %v; want ErrSameCircle", err, s.err)
	}
	if calls != 0 || !slices.EqualFunc(inv.Chain.Records(), inviterBefore, bytes.Equal) ||
		!slices.EqualFunc(req.Chain.Records(), joinerBefore, bytes.Equal) {
		t.Fatal("a stale inviter must not change either chain")
	}
}

// 加入方一侧：同一个圈的 JOIN，欢迎消息里的链不以本机的链为前缀（分叉或比本机旧）：拒绝，本机的链不变。
func TestJoinerRefusesNonPrefixSameCircle(t *testing.T) {
	inv, req, base := staleInviterCircle(t)
	commits := 0
	req.Commit = func([][]byte) error { commits++; return nil }
	joinerBefore := req.Chain.Records()
	// 邀请方在旧链上追加"Y 加 X"：和 X 的链同一个创世记录，但分叉了。
	if err := base.AddDevice(inv.Self, req.Self.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(inv.Self.Private, signed(inviterContext, exporterA))
	d := &pb.PairDecision{InviterKey: inv.Self.Public, InviterName: "笔记本", Signature: sig}
	w := &pb.PairWelcome{Chain: base.Records(), DiscoveryKey: discoveryKey, InviterKey: inv.Self.Public, Signature: sig}
	joined, err := pairFake(t, req, d, w)
	if !errors.Is(err, ErrStaleCircle) || joined != nil || commits != 0 ||
		!slices.EqualFunc(req.Chain.Records(), joinerBefore, bytes.Equal) {
		t.Fatalf("join = %+v, %v; want ErrStaleCircle and the joiner's chain untouched", joined, err)
	}
}

// runOldInviter 让 Join 和 oldInviter（或 resend 为 true 时原样重发 c 的旧链、不再加人）配一次。
func runOldInviter(t *testing.T, inviter *identity.Identity, c *chain.Chain, resend bool, req JoinRequest) (*Joined, error) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if !resend {
			_ = oldInviter(b, exporterA, inviter, c)
		} else {
			var hello pb.PairHello
			if wire.Read(b, &hello) == nil {
				msgB, key, err := cpace.Exchange(testCode, contextInfo(exporterA), hello.CpaceMsgA)
				if err == nil {
					tag, _ := confirmTag(key, roleResponder)
					_ = wire.Write(b, &pb.PairChallenge{CpaceMsgB: msgB, ResponderTag: tag})
					var confirm pb.PairConfirm
					if wire.Read(b, &confirm) == nil {
						_ = wire.Write(b, &pb.PairWelcome{
							Chain: c.Records(), DiscoveryKey: discoveryKey, InviterKey: inviter.Public,
							Signature: ed25519.Sign(inviter.Private, signed(inviterContext, exporterA)),
						})
					}
				}
			}
		}
		_, _ = io.Copy(io.Discard, b)
		b.Close()
	}()
	joined, err := Join(a, exporterA, req)
	a.Close()
	<-done
	return joined, err
}

// Y 建圈、加了 X；X 在自己的链上移除了 Y（单机了）。Y 是旧版本、链还是 [创世, 加入X]：
// X 已经是成员，Y 会原样重发旧链。X 必须拒绝，链不变。
func TestJoinOldInviterStaleSameCircle(t *testing.T) {
	y, x := mustID(t), mustID(t)
	yChain := circleOf(t, y, "笔记本", 1)
	if err := yChain.AddDevice(y, x.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	xChain := staleCopy(t, yChain)
	if err := xChain.RemoveDevice(x, y.KeyID, bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatal(err)
	}
	req := JoinRequest{Self: x, Name: "台式机", Code: testCode, Chain: xChain, Features: []string{FeatureCircle2}, DiscoveryKey: joinerDKey}
	_, err := runOldInviter(t, y, yChain, true, req)
	if !errors.Is(err, ErrStaleCircle) {
		t.Fatalf("err = %v, want ErrStaleCircle", err)
	}
}

// X 被移除过（链 [创世, 加入X, 移除X]，成员数 0），旧版本 Y 又把它加回来（[…, 加入X]）：对方的链以本机为前缀，照常加入。
func TestJoinOldInviterRejoinAfterRemoval(t *testing.T) {
	y, x := mustID(t), mustID(t)
	yChain := circleOf(t, y, "笔记本", 1)
	if err := yChain.AddDevice(y, x.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	if err := yChain.RemoveDevice(y, x.KeyID, bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatal(err)
	}
	xChain := staleCopy(t, yChain)
	req := JoinRequest{Self: x, Name: "台式机", Code: testCode, Chain: xChain, Features: []string{FeatureCircle2}, DiscoveryKey: joinerDKey}
	j, err := runOldInviter(t, y, yChain, false, req)
	if err != nil {
		t.Fatal(err)
	}
	if j.Mode != ModeJoin || !j.Chain.IsMember(x.KeyID) {
		t.Fatal("a removed device must be able to rejoin through an old inviter")
	}
}

// ---------- 合并（MERGE） ----------

var mergeFeatures = []string{chain.FeatureRename, FeatureCircle2, FeatureMerge}

// mergeSetup 建 B = {Y, B1} 的邀请方和 A = {X, A1, A2} 的加入方，双方都声明 merge，加入方已同意合并。
// commits 统计邀请方 Commit 的调用次数。
func mergeSetup(t *testing.T) (inv Invite, req JoinRequest, commits *int) {
	t.Helper()
	y, x := mustID(t), mustID(t)
	b, err := chain.New(y, "Y")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AddDevice(y, mustID(t).Public, "B1"); err != nil {
		t.Fatal(err)
	}
	a, err := chain.New(x, "X")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A1", "A2"} {
		if err := a.AddDevice(x, mustID(t).Public, name); err != nil {
			t.Fatal(err)
		}
	}
	commits = new(int)
	inv = Invite{
		Code: testCode, Self: y, Chain: b, DiscoveryKey: discoveryKey, RelayConfig: "portide-relay://inviter",
		Features: mergeFeatures, Commit: func([][]byte) error { *commits++; return nil },
		Adopt: func(*Adopted) error { return errors.New("must not adopt") },
	}
	req = JoinRequest{
		Self: x, Name: "X", Code: testCode, Features: mergeFeatures, Chain: a,
		DiscoveryKey: joinerDKey, RelayConfig: "portide-relay://joiner", MergeOK: true,
		Commit: func([][]byte) error { return errors.New("joiner must not commit on merge") },
	}
	return inv, req, commits
}

func memberNames(c *chain.Chain) []string {
	var names []string
	for _, m := range c.Members() {
		names = append(names, m.Name)
	}
	return names
}

func TestMerge(t *testing.T) {
	inv, req, commits := mergeSetup(t)
	genesis := inv.Chain.Records()[0]
	aBefore := req.Chain.Records()

	s, j, err := pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("join err = %v, serve err = %v", err, s.err)
	}
	if s.res.Mode != ModeMerge || !slices.Equal(s.res.Added, []string{"X", "A1", "A2"}) {
		t.Fatalf("serve result mode %q added %q", s.res.Mode, s.res.Added)
	}
	if j.Mode != ModeMerge || j.InviterKeyID != inv.Self.KeyID || j.InviterName != "Y" {
		t.Fatalf("joined mode %q inviter %q %q", j.Mode, j.InviterKeyID, j.InviterName)
	}
	if !slices.EqualFunc(j.Chain.Records(), inv.Chain.Records(), bytes.Equal) {
		t.Fatal("joiner must get the inviter's committed chain")
	}
	if got := memberNames(j.Chain); !slices.Equal(got, []string{"Y", "B1", "X", "A1", "A2"}) {
		t.Fatalf("members = %q", got)
	}
	if !bytes.Equal(j.Chain.Records()[0], genesis) {
		t.Fatal("the merged chain must keep B's genesis")
	}
	if *commits != 1 {
		t.Fatalf("inviter commits = %d, want 1", *commits)
	}
	if !bytes.Equal(j.DiscoveryKey, discoveryKey) || j.RelayConfig != inv.RelayConfig {
		t.Fatal("joiner must get B's discovery key and the inviter's relay config")
	}
	if !slices.EqualFunc(req.Chain.Records(), aBefore, bytes.Equal) {
		t.Fatal("Join must not change the caller's chain")
	}
}

// 邀请方已提交、欢迎消息丢了：加入方仍在 A 里，再配一次。B 不重复追加记录，Added 为空，加入方拿到同一条链。
func TestMergeRepairAfterLostWelcome(t *testing.T) {
	inv, req, commits := mergeSetup(t)
	if s, _, err := pairReq(t, inv, exporterA, req, exporterA); err != nil || s.err != nil {
		t.Fatalf("first merge: join %v, serve %v", err, s.err)
	}
	committed := inv.Chain.Len()
	// 加入方没收到欢迎消息：它的链还是 A（Join 不修改 req.Chain）。
	s, j, err := pairReq(t, inv, exporterB, req, exporterB)
	if err != nil || s.err != nil {
		t.Fatalf("re-merge: join %v, serve %v", err, s.err)
	}
	if *commits != 1 || len(s.res.Added) != 0 || s.res.Mode != ModeMerge {
		t.Fatalf("re-merge must not commit again: commits %d added %q mode %q", *commits, s.res.Added, s.res.Mode)
	}
	if inv.Chain.Len() != committed || len(j.Chain.Records()) != committed || j.Mode != ModeMerge {
		t.Fatalf("re-merge chain len %d / %d, want %d", inv.Chain.Len(), j.Chain.Len(), committed)
	}
}

func testMergeRefusedVersion(t *testing.T, inviterFeatures, joinerFeatures []string) {
	t.Helper()
	inv, req, commits := mergeSetup(t)
	inv.Features, req.Features = inviterFeatures, joinerFeatures
	before := inv.Chain.Records()
	s, j, err := pairReq(t, inv, exporterA, req, exporterA)
	if !errors.Is(err, ErrMergeUnsupported) || !errors.Is(s.err, ErrMergeUnsupported) || j != nil {
		t.Fatalf("join err = %v, serve err = %v; want ErrMergeUnsupported", err, s.err)
	}
	if *commits != 0 || !slices.EqualFunc(inv.Chain.Records(), before, bytes.Equal) {
		t.Fatal("a refused merge must not change the inviter's chain")
	}
}

// 加入方是 M5a（声明 circle2、没有 merge）：邀请方拒绝，加入方得到 ErrMergeUnsupported，邀请方的链不变。
func TestMergeJoinerWithoutMergeFeature(t *testing.T) {
	testMergeRefusedVersion(t, mergeFeatures, newFeatures)
}

// 邀请方是 M5a（声明 circle2、没有 merge，它 decide 返回 "merge unsupported"）：加入方得到 ErrMergeUnsupported。
func TestMergeInviterWithoutMergeFeature(t *testing.T) {
	testMergeRefusedVersion(t, newFeatures, mergeFeatures)
}

// mergeJoiner 模拟一个声明了 merge 的加入方（可能是恶意的）：报 cs，收到 MERGE 后把 records 当作来源圈发过去，返回邀请方的欢迎消息。
func mergeJoiner(rw io.ReadWriter, exporter []byte, self *identity.Identity, cs *pb.CircleState, records [][]byte) (*pb.PairWelcome, error) {
	msgA, st, err := cpace.Start(testCode, contextInfo(exporter))
	if err != nil {
		return nil, err
	}
	if err := wire.Write(rw, &pb.PairHello{CpaceMsgA: msgA}); err != nil {
		return nil, err
	}
	var ch pb.PairChallenge
	if err := wire.Read(rw, &ch); err != nil {
		return nil, err
	}
	key, err := st.Finish(ch.CpaceMsgB)
	if err != nil {
		return nil, err
	}
	tag, _ := confirmTag(key, roleInitiator)
	if err := wire.Write(rw, &pb.PairConfirm{
		InitiatorTag: tag, DeviceKey: self.Public, DeviceName: "X", Features: mergeFeatures, Circle: cs,
		Signature: ed25519.Sign(self.Private, signed(joinerContext, exporter)),
	}); err != nil {
		return nil, err
	}
	var d pb.PairDecision
	if err := wire.Read(rw, &d); err != nil {
		return nil, err
	}
	if d.Error != "" || d.Mode != pb.PairDecision_MERGE {
		return nil, errors.New("inviter did not decide to merge: " + d.Error)
	}
	if err := wire.Write(rw, &pb.PairMerge{Chain: records}); err != nil {
		return nil, err
	}
	var w pb.PairWelcome
	if err := wire.Read(rw, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// 加入方发来的 A 链不含加入方自己，或者和它在 PairConfirm 里报的 circle_id 不同：拒绝（PairWelcome.error），什么都不提交。
func TestMergeRejectsBadSourceChain(t *testing.T) {
	inv, req, commits := mergeSetup(t)
	a := req.Chain
	other := circleOf(t, mustID(t), "其他", 3) // 不含加入方
	cases := []struct {
		name    string
		cs      *pb.CircleState
		records [][]byte
	}{
		{"not a member", &pb.CircleState{Members: 3, CircleId: circleID(other), MergeOk: true}, other.Records()},
		{"other circle id", &pb.CircleState{Members: 3, CircleId: circleID(other), MergeOk: true}, a.Records()},
		{"inviter's circle", &pb.CircleState{Members: 3, CircleId: circleID(a), MergeOk: true}, inv.Chain.Records()},
		{"garbage", &pb.CircleState{Members: 3, CircleId: circleID(a), MergeOk: true}, [][]byte{[]byte("x")}},
	}
	before := inv.Chain.Records()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, q := net.Pipe()
			done := make(chan served, 1)
			go func() {
				res, err := Serve(q, exporterA, inv)
				q.Close()
				done <- served{res, err}
			}()
			w, err := mergeJoiner(p, exporterA, req.Self, tc.cs, tc.records)
			p.Close()
			s := <-done
			if err != nil || w.Error != "invalid chain" || s.err == nil {
				t.Fatalf("welcome = %+v, %v; serve err = %v; want an invalid chain refusal", w, err, s.err)
			}
			if *commits != 0 || !slices.EqualFunc(inv.Chain.Records(), before, bytes.Equal) {
				t.Fatal("a refused merge must not commit or change the inviter's chain")
			}
		})
	}
}

// 邀请方回来的 B′ 少了 A 的某个当前成员：加入方拒绝（错误信息含 "does not contain"），不切换。
func TestMergeJoinerChecksAllMembersBrought(t *testing.T) {
	_, req, _ := mergeSetup(t)
	y := mustID(t)
	b, err := chain.New(y, "Y")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range req.Chain.Members()[:2] { // X、A1，少了 A2
		if err := b.AddDevice(y, m.PublicKey, m.Name); err != nil {
			t.Fatal(err)
		}
	}
	sig := ed25519.Sign(y.Private, signed(inviterContext, exporterA))
	d := &pb.PairDecision{Mode: pb.PairDecision_MERGE, InviterKey: y.Public, InviterName: "Y", Signature: sig}
	w := &pb.PairWelcome{Chain: b.Records(), DiscoveryKey: discoveryKey, InviterKey: y.Public, Signature: sig}
	j, err := pairFake(t, req, d, w)
	if err == nil || j != nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("join = %+v, %v; want a missing member refusal", j, err)
	}
}

// relaySettingsOf 返回 self 签名的一份中继设置（两个中继，n 区分内容）和第一个中继的配置串。
func relaySettingsOf(t *testing.T, self *identity.Identity, n byte) ([]byte, string) {
	t.Helper()
	var cs []relay.Config
	for i := byte(0); i < 2; i++ {
		c := relay.Config{Addr: "relay.example.com:7443", Token: bytes.Repeat([]byte{n + i}, relay.TokenSize)}
		for j := range c.Fingerprint {
			c.Fingerprint[j] = n + i
		}
		cs = append(cs, c)
	}
	s, err := relayset.Sign(self.Private, 3, cs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s.Raw, cs[0].ConfigString()
}

// 欢迎消息两个方向都带中继设置：JOIN 时邀请方的到加入方，ADOPT 时加入方的到邀请方；relay_config 是第一个中继。
func TestWelcomeCarriesRelaySettings(t *testing.T) {
	inv := circleInvite(t, 2)
	inv.RelaySettings, inv.RelayConfig = relaySettingsOf(t, inv.Self, 1)
	req := circleJoiner(t, 1)
	s, joined, err := pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("join err = %v, serve err = %v", err, s.err)
	}
	if joined.Mode != ModeJoin || !bytes.Equal(joined.RelaySettings, inv.RelaySettings) || joined.RelayConfig != inv.RelayConfig {
		t.Fatalf("join: relay settings %x, relay config %q", joined.RelaySettings, joined.RelayConfig)
	}

	inv = circleInvite(t, 1)
	var adopted *Adopted
	inv.Adopt = func(a *Adopted) error { adopted = a; return nil }
	req = circleJoiner(t, 3)
	req.RelaySettings, req.RelayConfig = relaySettingsOf(t, req.Self, 5)
	s, joined, err = pairReq(t, inv, exporterA, req, exporterA)
	if err != nil || s.err != nil {
		t.Fatalf("adopt: join err = %v, serve err = %v", err, s.err)
	}
	if joined.Mode != ModeAdopt || adopted == nil ||
		!bytes.Equal(adopted.RelaySettings, req.RelaySettings) || adopted.RelayConfig != req.RelayConfig ||
		!bytes.Equal(joined.RelaySettings, req.RelaySettings) {
		t.Fatalf("adopt: adopted %+v, joined %+v", adopted, joined)
	}
}

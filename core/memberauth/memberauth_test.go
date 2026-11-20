package memberauth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"portide/core/identity"
	"portide/core/pb"
	"portide/core/wire"
)

var (
	circleKey = bytes.Repeat([]byte{1}, 32)
	otherKey  = bytes.Repeat([]byte{2}, 32)
	exporterA = bytes.Repeat([]byte{0xA}, ExporterLen)
	exporterB = bytes.Repeat([]byte{0xB}, ExporterLen)
)

func mustID(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type outcome struct {
	peer *Peer
	err  error
}

// run 在内存管道两端分别以发起方和接收方执行握手。出错的一方关闭自己那一端，让另一方解除阻塞。
func run(t *testing.T, dialer, listener Config, dialerExp, listenerExp []byte) (d, l outcome) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan outcome, 1)
	go func() {
		p, err := Exchange(b, listenerExp, listener)
		if err != nil {
			b.Close()
		}
		done <- outcome{p, err}
	}()
	p, err := Exchange(a, dialerExp, dialer)
	if err != nil {
		a.Close()
	}
	d = outcome{p, err}
	l = <-done
	a.Close()
	b.Close()
	return d, l
}

func admitAll(*Peer) error { return nil }

func cfg(id *identity.Identity, name string, dialer bool, keys ...[]byte) Config {
	return Config{Self: id, Name: name, Chain: [][]byte{[]byte(name + "-chain")}, CircleKeys: keys, Dialer: dialer, Admit: admitAll}
}

// fakeDialer 手工扮演发起方：用 key 发出圈子证明并读回接收方的证明，然后发出 hello。
// 返回发出 hello 之后从接收方读到的全部字节。
func fakeDialer(t *testing.T, rw net.Conn, key []byte, hello *pb.Hello) []byte {
	t.Helper()
	mac, err := gateMAC(key, roleDialer, exporterA)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.Write(rw, &pb.MemberGate{Mac: mac}); err != nil {
		t.Fatal(err)
	}
	var g pb.MemberGate
	if err := wire.Read(rw, &g); err != nil {
		t.Fatal(err)
	}
	if err := wire.Write(rw, hello); err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(rw)
	return rest
}

func signedHello(id *identity.Identity, role string, exporter []byte) *pb.Hello {
	return &pb.Hello{Version: ProtocolVersion, DeviceName: "x", DeviceKey: id.Public, Signature: ed25519.Sign(id.Private, identityMessage(role, exporter))}
}

func TestMembersLearnEachOther(t *testing.T) {
	laptop, desktop := mustID(t), mustID(t)
	d, l := run(t, cfg(laptop, "笔记本", true, circleKey), cfg(desktop, "台式机", false, circleKey), exporterA, exporterA)
	if d.err != nil || l.err != nil {
		t.Fatalf("dialer err %v, listener err %v", d.err, l.err)
	}
	if d.peer.KeyID != desktop.KeyID || d.peer.Name != "台式机" || string(d.peer.Chain[0]) != "台式机-chain" {
		t.Fatalf("dialer learned %+v", d.peer)
	}
	if l.peer.KeyID != laptop.KeyID || l.peer.Name != "笔记本" {
		t.Fatalf("listener learned %+v", l.peer)
	}
}

func TestPreviousCircleKeyAccepted(t *testing.T) {
	// 接收方已轮换到新密钥，但仍接受旧密钥；发起方还只有旧密钥。
	d, l := run(t, cfg(mustID(t), "旧", true, otherKey), cfg(mustID(t), "新", false, circleKey, otherKey), exporterA, exporterA)
	if d.err != nil || l.err != nil {
		t.Fatalf("dialer err %v, listener err %v", d.err, l.err)
	}
}

func TestWrongCircleRejectedBeforeAnyIdentity(t *testing.T) {
	d, l := run(t, cfg(mustID(t), "外人", true, otherKey), cfg(mustID(t), "台式机", false, circleKey), exporterA, exporterA)
	if !errors.Is(l.err, ErrNotInCircle) {
		t.Fatalf("listener err = %v, want ErrNotInCircle", l.err)
	}
	if d.err == nil || d.peer != nil {
		t.Fatalf("outsider must learn nothing, got %+v / %v", d.peer, d.err)
	}
}

func TestDifferentTLSSessionsFail(t *testing.T) {
	// 中间人终止了两条 TLS 连接，两边的导出值不同。
	_, l := run(t, cfg(mustID(t), "a", true, circleKey), cfg(mustID(t), "b", false, circleKey), exporterA, exporterB)
	if !errors.Is(l.err, ErrNotInCircle) {
		t.Fatalf("listener err = %v, want ErrNotInCircle", l.err)
	}
}

// 外人主动连进来：只能发一个伪造的证明，接收方验证失败后不会发出任何字节。
func TestProberReceivesNothing(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	done := make(chan error, 1)
	go func() {
		_, err := Exchange(b, exporterA, cfg(mustID(t), "台式机", false, circleKey))
		b.Close()
		done <- err
	}()
	if err := wire.Write(a, &pb.MemberGate{Mac: bytes.Repeat([]byte{9}, 32)}); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(a)
	if len(got) != 0 {
		t.Fatalf("listener sent %d bytes to a prober", len(got))
	}
	if err := <-done; !errors.Is(err, ErrNotInCircle) {
		t.Fatalf("listener err = %v", err)
	}
}

// 冒充接收方：把发起方的证明原样反射回去。角色不同，发起方必须拒绝，并且不再发出身份。
func TestReflectedGateRejected(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		_, err := Exchange(a, exporterA, cfg(mustID(t), "笔记本", true, circleKey))
		a.Close()
		done <- err
	}()
	var g pb.MemberGate
	if err := wire.Read(b, &g); err != nil {
		t.Fatal(err)
	}
	if err := wire.Write(b, &g); err != nil {
		t.Fatal(err)
	}
	if rest, _ := io.ReadAll(b); len(rest) != 0 {
		t.Fatalf("dialer revealed %d bytes after a reflected gate", len(rest))
	}
	if err := <-done; !errors.Is(err, ErrNotInCircle) {
		t.Fatalf("dialer err = %v, want ErrNotInCircle", err)
	}
}

func TestIdentityBoundToConnectionAndRole(t *testing.T) {
	id := mustID(t)
	h := &pb.Hello{Version: ProtocolVersion, DeviceKey: id.Public, Signature: ed25519.Sign(id.Private, identityMessage(roleDialer, exporterA))}
	if _, err := verifyHello(h, roleDialer, exporterA); err != nil {
		t.Fatalf("valid hello rejected: %v", err)
	}
	if _, err := verifyHello(h, roleDialer, exporterB); !errors.Is(err, ErrBadIdentity) {
		t.Fatalf("replay on another connection: err = %v", err)
	}
	if _, err := verifyHello(h, roleListener, exporterA); !errors.Is(err, ErrBadIdentity) {
		t.Fatalf("reflected role: err = %v", err)
	}
	h.Version = 1
	if _, err := verifyHello(h, roleDialer, exporterA); err == nil {
		t.Fatal("old protocol version must be rejected")
	}
}

// 对方能通过圈子证明（例如持有轮换前的发现密钥），但调用方判定它已不是成员：接收方不能出示自己的身份。
func TestAdmitRejectionRevealsNothing(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	errNope := errors.New("not a member")
	listener := cfg(mustID(t), "台式机", false, circleKey)
	listener.Admit = func(*Peer) error { return errNope }
	done := make(chan error, 1)
	go func() {
		_, err := Exchange(b, exporterA, listener)
		b.Close()
		done <- err
	}()
	if rest := fakeDialer(t, a, circleKey, signedHello(mustID(t), roleDialer, exporterA)); len(rest) != 0 {
		t.Fatalf("listener revealed %d bytes to a rejected peer", len(rest))
	}
	if err := <-done; !errors.Is(err, errNope) {
		t.Fatalf("listener err = %v, want the Admit error", err)
	}
}

// 对方的身份证明无效时，接收方同样保持沉默。
func TestListenerSilentAfterBadHello(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	done := make(chan error, 1)
	go func() {
		_, err := Exchange(b, exporterA, cfg(mustID(t), "台式机", false, circleKey))
		b.Close()
		done <- err
	}()
	bad := signedHello(mustID(t), roleDialer, exporterB) // 签名绑定的是另一条连接
	if rest := fakeDialer(t, a, circleKey, bad); len(rest) != 0 {
		t.Fatalf("listener revealed %d bytes after an invalid hello", len(rest))
	}
	if err := <-done; !errors.Is(err, ErrBadIdentity) {
		t.Fatalf("listener err = %v, want ErrBadIdentity", err)
	}
}

// 发起方只信任自己的当前密钥：冒充的接收方用旧密钥回应时，发起方必须拒绝，并且不发出身份。
func TestDialerOnlyTrustsCurrentKey(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		_, err := Exchange(a, exporterA, cfg(mustID(t), "笔记本", true, circleKey, otherKey))
		a.Close()
		done <- err
	}()
	var g pb.MemberGate
	if err := wire.Read(b, &g); err != nil {
		t.Fatal(err)
	}
	old, err := gateMAC(otherKey, roleListener, exporterA)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.Write(b, &pb.MemberGate{Mac: old}); err != nil {
		t.Fatal(err)
	}
	if rest, _ := io.ReadAll(b); len(rest) != 0 {
		t.Fatalf("dialer revealed %d bytes to an old-key listener", len(rest))
	}
	if err := <-done; !errors.Is(err, ErrNotInCircle) {
		t.Fatalf("dialer err = %v, want ErrNotInCircle", err)
	}
}

// 空或长度不对的圈子密钥会派生出公开可算的认证密钥，必须直接报错；Admit 也是必填的。
func TestConfigValidation(t *testing.T) {
	for _, keys := range [][][]byte{{nil}, {bytes.Repeat([]byte{1}, 31)}, {circleKey, {}}} {
		c := cfg(mustID(t), "x", true, keys...)
		if _, err := Exchange(&bytes.Buffer{}, exporterA, c); err == nil {
			t.Fatalf("keys %v must be rejected", keys)
		}
	}
	c := cfg(mustID(t), "x", true, circleKey)
	c.Admit = nil
	if _, err := Exchange(&bytes.Buffer{}, exporterA, c); err == nil {
		t.Fatal("a nil Admit must be rejected")
	}
}

// 对方在圈子证明之前就发一个声称很大的长度头：接收方必须立刻拒绝，不等待消息体，也不发出任何内容。
func TestOversizedGateRejected(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	done := make(chan error, 1)
	go func() {
		_, err := Exchange(b, exporterA, cfg(mustID(t), "台式机", false, circleKey))
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
		t.Fatalf("listener sent %d bytes to an oversized gate", len(got))
	}
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("listener err = %v, want an exceeds-limit error", err)
	}
}

// 可选功能（M3c）：双方互相看到对方声明的功能；没有声明（旧版本）时为空。
func TestFeaturesAreExchanged(t *testing.T) {
	laptop, desktop := mustID(t), mustID(t)
	dc, lc := cfg(laptop, "笔记本", true, circleKey), cfg(desktop, "台式机", false, circleKey)
	dc.Features = []string{"rename"}
	d, l := run(t, dc, lc, exporterA, exporterA)
	if d.err != nil || l.err != nil {
		t.Fatalf("dialer err %v, listener err %v", d.err, l.err)
	}
	if len(l.peer.Features) != 1 || l.peer.Features[0] != "rename" {
		t.Fatalf("listener saw features %q", l.peer.Features)
	}
	if len(d.peer.Features) != 0 {
		t.Fatalf("dialer saw features %q from a peer that declared none", d.peer.Features)
	}
}

// 接收方接受两把密钥；发起方用第二把：Peer.GateKey 是第二把，接收方出示的是 ChainFor(第二把) 的链。
func TestChainForGateKey(t *testing.T) {
	listener := cfg(mustID(t), "帮手", false, circleKey, otherKey)
	var asked []byte
	listener.ChainFor = func(key []byte) [][]byte {
		asked = key
		if bytes.Equal(key, otherKey) {
			return [][]byte{[]byte("old-chain")}
		}
		return listener.Chain
	}
	d, l := run(t, cfg(mustID(t), "旧成员", true, otherKey), listener, exporterA, exporterA)
	if d.err != nil || l.err != nil {
		t.Fatalf("dialer err %v, listener err %v", d.err, l.err)
	}
	if !bytes.Equal(l.peer.GateKey, otherKey) || !bytes.Equal(asked, otherKey) {
		t.Fatalf("listener GateKey = %x, ChainFor got %x; want the second key", l.peer.GateKey, asked)
	}
	if !bytes.Equal(d.peer.GateKey, otherKey) {
		t.Fatalf("dialer GateKey = %x, want its own key", d.peer.GateKey)
	}
	if len(d.peer.Chain) != 1 || string(d.peer.Chain[0]) != "old-chain" {
		t.Fatalf("listener showed chain %q, want ChainFor's", d.peer.Chain)
	}
	// 用第一把密钥时出示 Config.Chain。
	d, l = run(t, cfg(mustID(t), "成员", true, circleKey), listener, exporterA, exporterA)
	if d.err != nil || l.err != nil || string(d.peer.Chain[0]) != "帮手-chain" || !bytes.Equal(l.peer.GateKey, circleKey) {
		t.Fatalf("first key: dialer %+v / %v, listener err %v", d.peer, d.err, l.err)
	}
}

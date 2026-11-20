package relayserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/relay"
)

// startTCPServer 起一个 tcpPacketConn，返回它、地址和中继指纹。
func startTCPServer(t *testing.T, maxPending int) (*tcpPacketConn, string, [32]byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := relay.ServerCertificate(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conf := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{relay.ALPNTCP}}
	p := newTCPPacketConn(ln, conf, maxPending)
	t.Cleanup(func() { p.Close() })
	return p, ln.Addr().String(), relay.Fingerprint(pub)
}

func dialTCP(t *testing.T, addr string, fp [32]byte) net.PacketConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := relay.DialTCP(ctx, (&net.Dialer{}).DialContext, addr, fp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func readOne(t *testing.T, p net.PacketConn) ([]byte, net.Addr) {
	t.Helper()
	_ = p.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 2000)
	n, a, err := p.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n], a
}

func TestTCPPacketConnRoundTrip(t *testing.T) {
	srv, addr, fp := startTCPServer(t, 16)
	c1, c2 := dialTCP(t, addr, fp), dialTCP(t, addr, fp)
	if _, err := c1.WriteTo([]byte("one"), nil); err != nil {
		t.Fatal(err)
	}
	got, a1 := readOne(t, srv)
	if string(got) != "one" || a1.String() != c1.LocalAddr().String() {
		t.Fatalf("got %q from %v, want one from %v", got, a1, c1.LocalAddr())
	}
	if _, err := c2.WriteTo([]byte("two"), nil); err != nil {
		t.Fatal(err)
	}
	got, a2 := readOne(t, srv)
	if string(got) != "two" || a2.String() != c2.LocalAddr().String() {
		t.Fatalf("got %q from %v", got, a2)
	}
	if srv.Count() != 2 {
		t.Fatalf("Count = %d", srv.Count())
	}
	for i, x := range []struct {
		to net.Addr
		c  net.PacketConn
	}{{a1, c1}, {a2, c2}, {a1, c1}} {
		msg := []byte{byte('a' + i)}
		if _, err := srv.WriteTo(msg, x.to); err != nil {
			t.Fatal(err)
		}
		if got, _ := readOne(t, x.c); string(got) != string(msg) {
			t.Fatalf("client got %q", got)
		}
	}
	if srv.LocalAddr().String() != addr {
		t.Fatalf("LocalAddr = %v", srv.LocalAddr())
	}
}

func TestTCPPacketConnBadFrame(t *testing.T) {
	srv, addr, fp := startTCPServer(t, 16)
	// 长度为 0 的帧：客户端的 WriteTo 不允许，所以 bad 自己做外层 TLS，直接写原始字节
	conf := relay.ClientTLS(fp)
	conf.NextProtos = []string{relay.ALPNTCP}
	raw, err := tls.Dial("tcp", addr, conf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	good := dialTCP(t, addr, fp)
	// 先让两条都登记
	if err := relay.WriteFrame(raw, []byte("x")); err != nil {
		t.Fatal(err)
	}
	_, badAddr := readOne(t, srv)
	good.WriteTo([]byte("y"), nil)
	_, goodAddr := readOne(t, srv)

	if _, err := raw.Write([]byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := srv.WriteTo([]byte("z"), badAddr); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bad connection was not dropped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := srv.WriteTo([]byte("ok"), goodAddr); err != nil {
		t.Fatal(err)
	}
	if got, _ := readOne(t, good); string(got) != "ok" {
		t.Fatalf("good got %q", got)
	}
}

func TestTCPPacketConnWriteAfterClose(t *testing.T) {
	srv, addr, fp := startTCPServer(t, 16)
	c := dialTCP(t, addr, fp)
	c.WriteTo([]byte("x"), nil)
	_, a := readOne(t, srv)
	c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := srv.WriteTo([]byte("z"), a); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("WriteTo to a closed client kept succeeding")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTCPPacketConnWrongFingerprint(t *testing.T) {
	_, addr, fp := startTCPServer(t, 16)
	fp[0] ^= 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := relay.DialTCP(ctx, (&net.Dialer{}).DialContext, addr, fp); err == nil {
		c.Close()
		t.Fatal("wrong fingerprint must fail")
	}
}

func TestTCPPacketConnPendingCap(t *testing.T) {
	srv, addr, fp := startTCPServer(t, 1)
	c1 := dialTCP(t, addr, fp)
	c1.WriteTo([]byte("x"), nil)
	_, a1 := readOne(t, srv)
	// 第一条还没 Established，第二条应被拒绝
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, err := relay.DialTCP(ctx, (&net.Dialer{}).DialContext, addr, fp); err == nil {
		c.WriteTo([]byte("y"), nil)
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, err := c.ReadFrom(make([]byte, 10)); err == nil {
			t.Fatal("second pending connection should be closed")
		}
		c.Close()
	}
	srv.Established(a1)
	c3 := dialTCP(t, addr, fp)
	c3.WriteTo([]byte("z"), nil)
	if got, _ := readOne(t, srv); string(got) != "z" {
		t.Fatalf("got %q", got)
	}
}

func TestTCPPacketConnOversizePacket(t *testing.T) {
	srv, addr, fp := startTCPServer(t, 16)
	big, other := dialTCP(t, addr, fp), dialTCP(t, addr, fp)
	big.WriteTo(make([]byte, 100), nil)
	other.WriteTo([]byte("hi"), nil)
	// 缓冲区只有 10 字节：big 的包让它断开，other 的包正常收到
	_ = srv.SetReadDeadline(time.Now().Add(5 * time.Second))
	small := make([]byte, 10)
	n, a, err := srv.ReadFrom(small)
	if err != nil || string(small[:n]) != "hi" || a.String() != other.LocalAddr().String() {
		t.Fatalf("ReadFrom = %d %v %v", n, a, err)
	}
	// 两个包的到达顺序不定：再读一次，确保 big 的包也被处理过
	_ = srv.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	srv.ReadFrom(small)
	big.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := big.ReadFrom(small); err == nil {
		t.Fatal("oversize sender should be disconnected")
	}
	if _, err := srv.WriteTo([]byte("ok"), a); err != nil {
		t.Fatal(err)
	}
	if got, _ := readOne(t, other); string(got) != "ok" {
		t.Fatalf("other got %q", got)
	}
}

func TestTCPPacketConnCloseAddr(t *testing.T) {
	srv, addr, fp := startTCPServer(t, 1)
	c := dialTCP(t, addr, fp)
	c.WriteTo([]byte("x"), nil)
	_, a := readOne(t, srv)
	srv.Established(a)
	srv.CloseAddr(a)
	if srv.Count() != 0 {
		t.Fatalf("Count = %d", srv.Count())
	}
	if _, err := srv.WriteTo([]byte("z"), a); err == nil {
		t.Fatal("WriteTo after CloseAddr must fail")
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := c.ReadFrom(make([]byte, 10)); err == nil {
		t.Fatal("client should see the connection closed")
	}
	srv.CloseAddr(a) // 未知地址：无操作
}

// stallListener 模拟对端不读、本端发送缓冲区已满：stall 置位后，连接上的 Write 一直阻塞到连接被关（最多 10 秒）。
// tls.Conn.Close 会先写 close_notify，于是会被卡住；直接关底层连接则不会。
type stallListener struct {
	net.Listener
	stall *atomic.Bool
}

type stallConn struct {
	net.Conn
	stall  *atomic.Bool
	once   sync.Once
	closed chan struct{}
}

func (l stallListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &stallConn{Conn: c, stall: l.stall, closed: make(chan struct{})}, nil
}

func (c *stallConn) Write(b []byte) (int, error) {
	if c.stall.Load() {
		select {
		case <-c.closed:
			return 0, net.ErrClosed
		case <-time.After(10 * time.Second):
			return 0, errors.New("stalled write gave up")
		}
	}
	return c.Conn.Write(b)
}

func (c *stallConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// startStallServer 同 startTCPServer，但所有连接的写在 stall 置位后被卡住。
func startStallServer(t *testing.T) (*tcpPacketConn, string, [32]byte, *atomic.Bool) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	cert, err := relay.ServerCertificate(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stall := &atomic.Bool{}
	conf := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{relay.ALPNTCP}}
	p := newTCPPacketConn(stallListener{ln, stall}, conf, 16)
	t.Cleanup(func() { p.Close() })
	return p, ln.Addr().String(), relay.Fingerprint(pub), stall
}

// 对端不读时，因包太大而断开它不能拖住 ReadFrom（所有 TCP 连接共用这一个读循环）。
func TestTCPPacketConnOversizeFromStalledPeer(t *testing.T) {
	srv, addr, fp, stall := startStallServer(t)
	stalled, good := dialTCP(t, addr, fp), dialTCP(t, addr, fp)
	stalled.WriteTo([]byte("x"), nil)
	readOne(t, srv)
	good.WriteTo([]byte("y"), nil)
	readOne(t, srv)
	stall.Store(true)

	if _, err := stalled.WriteTo(make([]byte, 1600), nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if _, err := good.WriteTo([]byte("hi"), nil); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	_ = srv.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		n, _, err := srv.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		if string(buf[:n]) == "hi" {
			break
		}
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("a stalled peer delayed another connection's packet by %v", d)
	}
}

// Close 和 CloseAddr 不能因为对端不读而卡住。
func TestTCPPacketConnCloseWithStalledPeer(t *testing.T) {
	for _, closeAddr := range []bool{true, false} {
		srv, addr, fp, stall := startStallServer(t)
		c := dialTCP(t, addr, fp)
		c.WriteTo([]byte("x"), nil)
		_, a := readOne(t, srv)
		stall.Store(true)
		start := time.Now()
		if closeAddr {
			srv.CloseAddr(a)
		} else {
			srv.Close()
		}
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Fatalf("closeAddr=%v took %v with a stalled peer", closeAddr, d)
		}
	}
}

// 比 quic-go 的读缓冲区（1452 字节）大得多的帧直接断开，不交给 ReadFrom。
func TestTCPPacketConnFrameLimit(t *testing.T) {
	srv, addr, fp := startTCPServer(t, 16)
	c := dialTCP(t, addr, fp)
	c.WriteTo(make([]byte, 1500), nil)
	if got, _ := readOne(t, srv); len(got) != 1500 {
		t.Fatalf("got %d bytes", len(got))
	}
	c.WriteTo(make([]byte, tcpMaxFrame+1), nil)
	_ = srv.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := srv.ReadFrom(make([]byte, 0xFFFF)); err == nil {
		t.Fatalf("a %d-byte frame was delivered (%d bytes)", tcpMaxFrame+1, n)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := c.ReadFrom(make([]byte, 10)); err == nil {
		t.Fatal("the sender of an oversize frame should be disconnected")
	}
}

// sameRemoteListener 让所有连接报告同一个 RemoteAddr，模拟新连接复用了旧连接的源 ip:port。
type sameRemoteListener struct{ net.Listener }

type fixedRemoteConn struct{ net.Conn }

func (fixedRemoteConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 40000}
}

func (l sameRemoteListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return fixedRemoteConn{c}, nil
}

// 旧连接（a1）迟到的 Established / CloseAddr / WriteTo 不能落到复用了同一源地址的新连接上。
func TestTCPPacketConnAddrReuse(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	cert, err := relay.ServerCertificate(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conf := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{relay.ALPNTCP}}
	srv := newTCPPacketConn(sameRemoteListener{ln}, conf, 1)
	t.Cleanup(func() { srv.Close() })
	addr, fp := ln.Addr().String(), relay.Fingerprint(pub)

	c1 := dialTCP(t, addr, fp)
	c1.WriteTo([]byte("1"), nil)
	_, a1 := readOne(t, srv)
	c1.Close()
	for deadline := time.Now().Add(5 * time.Second); srv.Count() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("old connection was not dropped")
		}
	}
	c2 := dialTCP(t, addr, fp)
	c2.WriteTo([]byte("2"), nil)
	_, a2 := readOne(t, srv)
	if a1.String() != a2.String() {
		t.Fatalf("setup: %v != %v", a1, a2)
	}

	if _, err := srv.WriteTo([]byte("stale"), a1); err == nil {
		t.Fatal("WriteTo with the old connection's address must fail")
	}
	// a1 的 Established 不能让 c2 不再算"未完成"：上限 1，c2 仍占着，第三条应被拒
	srv.Established(a1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, err := relay.DialTCP(ctx, (&net.Dialer{}).DialContext, addr, fp); err == nil {
		c.WriteTo([]byte("3"), nil)
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, err := c.ReadFrom(make([]byte, 10)); err == nil {
			t.Fatal("a third pending connection should be closed: Established(a1) leaked to c2")
		}
		c.Close()
	}
	srv.CloseAddr(a1)
	if srv.Count() != 1 {
		t.Fatalf("CloseAddr(a1) closed the new connection: Count = %d", srv.Count())
	}
	if _, err := srv.WriteTo([]byte("ok"), a2); err != nil {
		t.Fatal(err)
	}
	if got, _ := readOne(t, c2); string(got) != "ok" {
		t.Fatalf("c2 got %q", got)
	}
}

// quic-go 对没有 SetReadBuffer/SetWriteBuffer 的 PacketConn 会打一条误导人的 UDP-Buffer-Sizes 警告。
func TestTCPConnsHaveBufferSetters(t *testing.T) {
	type bufferSetter interface {
		SetReadBuffer(int) error
		SetWriteBuffer(int) error
	}
	srv, addr, fp := startTCPServer(t, 16)
	c := dialTCP(t, addr, fp)
	for _, pc := range []net.PacketConn{srv, c} {
		bs, ok := pc.(bufferSetter)
		if !ok {
			t.Fatalf("%T lacks SetReadBuffer/SetWriteBuffer", pc)
		}
		if bs.SetReadBuffer(7<<20) != nil || bs.SetWriteBuffer(7<<20) != nil {
			t.Fatalf("%T buffer setters must be no-ops", pc)
		}
	}
}

// 在单独的进程里只起 TCP 两侧的 quic.Transport（警告用 sync.Once，只在进程里第一次出现），
// 确认没有 UDP-Buffer-Sizes 警告。
func TestTCPTransportNoBufferWarning(t *testing.T) {
	if os.Getenv("PORTIDE_TCP_BUFFER_CHILD") == "1" {
		tcpTransportChild(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTCPTransportNoBufferWarning$", "-test.count=1")
	cmd.Env = append(os.Environ(), "PORTIDE_TCP_BUFFER_CHILD=1", "QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "UDP-Buffer-Sizes") {
		t.Fatalf("quic-go warned about UDP buffer sizes on the TCP transport:\n%s", out)
	}
}

func tcpTransportChild(t *testing.T) {
	srv, addr, fp := startTCPServer(t, 16)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	cert, err := relay.ServerCertificate(priv)
	if err != nil {
		t.Fatal(err)
	}
	conf := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{relay.ALPN}, MinVersion: tls.VersionTLS13}
	trS := &quic.Transport{Conn: srv}
	defer trS.Close()
	if _, err := trS.Listen(conf, nil); err != nil {
		t.Fatal(err)
	}
	trC := &quic.Transport{Conn: dialTCP(t, addr, fp)}
	defer trC.Close()
	if _, err := trC.Listen(conf, nil); err != nil {
		t.Fatal(err)
	}
}

package relayserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/relay"
)

// blockUDP 模拟 UDP 被防火墙丢弃：一直等到 ctx 结束。
func blockUDP(ctx context.Context, _ string, _ *tls.Config) (*quic.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// countingTCP 返回一个记录拨号次数的 TCP 拨号函数。
func countingTCP(n *atomic.Int32) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		n.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
}

// relayCfg 返回 r 的配置，TCP 端口指向它的 TCP 监听。
func relayCfg(t *testing.T, r *tcpRelay) relay.Config {
	t.Helper()
	cfg := r.cfg
	_, port, err := net.SplitHostPort(r.tcpAddr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TCPPort = port
	return cfg
}

func (r *tcpRelay) clientCount() int {
	r.srv.mu.Lock()
	defer r.srv.mu.Unlock()
	return len(r.srv.clients)
}

func (r *tcpRelay) tcpCount() int {
	r.srv.mu.Lock()
	p := r.srv.tcpConn
	r.srv.mu.Unlock()
	if p == nil {
		return 0
	}
	return p.Count()
}

func TestConnectUsesUDPWhenAvailable(t *testing.T) {
	r := startRelayAll(t, Limits{}, nil)
	var tcpDials atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := relay.Connect(ctx, relay.Dialer{UDP: dialer(t), TCP: countingTCP(&tcpDials)}, relayCfg(t, r), mustID(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Transport() != "udp" || c.ObservedAddr() == "" {
		t.Fatalf("transport = %q, observed = %q", c.Transport(), c.ObservedAddr())
	}
	if n := tcpDials.Load(); n != 0 {
		t.Fatalf("TCP dialed %d times while UDP works", n)
	}
}

func TestConnectFallsBackToTCP(t *testing.T) {
	r := startRelayAll(t, Limits{}, nil)
	aID, bID := mustID(t), mustID(t)
	a := connect(t, r.testRelay, aID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	b, err := relay.Connect(ctx, relay.Dialer{UDP: blockUDP, TCPDelay: 100 * time.Millisecond}, relayCfg(t, r), bID)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("TCP must wait for TCPDelay")
	}
	if b.Transport() != "tcp" || b.ObservedAddr() != "" {
		t.Fatalf("transport = %q, observed = %q", b.Transport(), b.ObservedAddr())
	}
	// 经 TCP 的客户端照常订阅和开会话。
	if err := b.Subscribe([][]byte{relay.RouteID(aID.Public)}); err != nil {
		t.Fatal(err)
	}
	if e := nextEvent(t, b); !e.Online || !bytes.Equal(e.Route, relay.RouteID(aID.Public)) {
		t.Fatalf("online event = %+v", e)
	}
	accepted := make(chan net.PacketConn, 1)
	go func() {
		in, err := a.Accept(ctx)
		if err != nil {
			t.Error(err)
		}
		accepted <- in
	}()
	out, err := b.Open(ctx, relay.RouteID(aID.Public))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	in := <-accepted
	if in == nil {
		t.FailNow()
	}
	defer in.Close()
	if _, err := out.WriteTo([]byte("经 TCP 发出"), relay.Addr{}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	_ = in.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, _, err := in.ReadFrom(buf); err != nil || string(buf[:n]) != "经 TCP 发出" {
		t.Fatalf("A got %q %v", buf[:n], err)
	}

	// Close 一起关掉 TCP 连接：中继那边的 TCP 连接随之消失。
	if r.tcpCount() != 1 {
		t.Fatalf("tcp conns = %d", r.tcpCount())
	}
	b.Close()
	eventually(t, 5*time.Second, "Close must close the TCP connection", func() bool { return r.tcpCount() == 0 })
}

// 经 TCP 的 QUIC 连接自己断了（这里由中继踢掉）：客户端的 TCP 连接和 Transport 也随之关掉。
func TestConnectTCPClosedWhenQUICDies(t *testing.T) {
	r := startRelayAll(t, Limits{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var closed atomic.Bool
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &closeSpy{Conn: conn, closed: &closed}, nil
	}
	c, err := relay.Connect(ctx, relay.Dialer{UDP: blockUDP, TCP: dial, TCPDelay: time.Millisecond}, relayCfg(t, r), mustID(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r.srv.mu.Lock()
	for _, cl := range r.srv.clients {
		_ = cl.conn.CloseWithError(codeClosed, "") // 只关 QUIC
	}
	r.srv.mu.Unlock()
	eventually(t, 5*time.Second, "the client must close its TCP conn with the QUIC conn", closed.Load)
}

// closeSpy 记录客户端是否关闭了自己这一侧的 TCP 连接。
type closeSpy struct {
	net.Conn
	closed *atomic.Bool
}

func (c *closeSpy) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// UDP 和 TCP 几乎同时握手成功：只有一条登记，另一条在登记前就被关掉（Review Focus 1）。
func TestConnectRaceKeepsOne(t *testing.T) {
	r := startRelayAll(t, Limits{}, nil)
	cfg := relayCfg(t, r)
	udp := dialer(t)
	var clients []*relay.Client
	tcpWins := 0
	const rounds = 8
	for i := 0; i < rounds; i++ {
		// UDP 晚一点点出发，让两边的先后在各轮之间变化
		var mu sync.Mutex
		var udpConns []*quic.Conn
		delay := time.Duration(i) * 3 * time.Millisecond
		dial := func(ctx context.Context, addr string, tlsConf *tls.Config) (*quic.Conn, error) {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			conn, err := udp(ctx, addr, tlsConf)
			if conn != nil {
				mu.Lock()
				udpConns = append(udpConns, conn)
				mu.Unlock()
			}
			return conn, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := relay.Connect(ctx, relay.Dialer{UDP: dial, TCPDelay: time.Nanosecond}, cfg, mustID(t))
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		clients = append(clients, c)
		if c.Transport() == "tcp" {
			tcpWins++
			// 输掉的 UDP 连接（如果握手成功了）必须已经关掉，不留半开的 QUIC 连接
			mu.Lock()
			for _, uc := range udpConns {
				select {
				case <-uc.Context().Done():
				default:
					t.Fatal("the losing UDP conn must be closed")
				}
			}
			mu.Unlock()
		}
		if n := r.clientCount(); n != i+1 {
			t.Fatalf("round %d: relay has %d clients, want %d", i, n, i+1)
		}
		// 输掉的 TCP 连接也被关掉：中继上的 TCP 连接数等于经 TCP 登记的设备数
		want := tcpWins
		eventually(t, 3*time.Second, "the losing TCP conn must be closed", func() bool { return r.tcpCount() == want })
	}
	t.Logf("TCP won %d of %d rounds", tcpWins, rounds)

	// 两边都握手成功的确定情形：UDP 不理会取消，握手完成后故意晚一点才交回连接，此时 TCP 已经赢了。
	// 这条晚到的 UDP 连接必须在 Connect 返回前就被关掉，绝不能登记。
	var late *quic.Conn
	stubborn := func(ctx context.Context, addr string, tlsConf *tls.Config) (*quic.Conn, error) {
		conn, err := udp(context.WithoutCancel(ctx), addr, tlsConf)
		<-ctx.Done()
		late = conn
		return conn, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	c, err := relay.Connect(ctx, relay.Dialer{UDP: stubborn, TCPDelay: time.Nanosecond}, cfg, mustID(t))
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	clients = append(clients, c)
	if c.Transport() != "tcp" || late == nil {
		t.Fatalf("transport = %q, late = %v", c.Transport(), late)
	}
	select {
	case <-late.Context().Done():
	default:
		t.Fatal("a UDP conn that finished its handshake after TCP won must be closed before Connect returns")
	}

	time.Sleep(300 * time.Millisecond)
	if n := r.clientCount(); n != len(clients) {
		t.Fatalf("relay has %d clients after a while, want %d", n, len(clients))
	}
	if n := r.tcpCount(); n != tcpWins+1 {
		t.Fatalf("relay has %d TCP conns, want %d", n, tcpWins+1)
	}
	// 返回的客户端都可用
	for _, c := range clients {
		if err := c.Subscribe(nil); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.Done():
			t.Fatal("a returned client must stay connected")
		default:
		}
	}
}

func TestConnectBothFail(t *testing.T) {
	r := startRelayAll(t, Limits{}, nil)
	cfg := r.cfg
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, cfg.TCPPort, _ = net.SplitHostPort(ln.Addr().String())
	ln.Close() // 这个端口没人监听
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = relay.Connect(ctx, relay.Dialer{UDP: blockUDP, TCPDelay: 50 * time.Millisecond}, cfg, mustID(t))
	if !errors.Is(err, relay.ErrNoResponse) {
		t.Fatalf("err = %v, want ErrNoResponse", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v must keep the underlying UDP error", err)
	}
}

// 指纹不对是明确的错误，不能被说成"没有回应"。
func TestConnectFingerprintMismatch(t *testing.T) {
	r := startRelayAll(t, Limits{}, nil)
	cfg := relayCfg(t, r)
	other, _, _ := ed25519.GenerateKey(nil)
	cfg.Fingerprint = relay.Fingerprint(other)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var tcpDials atomic.Int32
	if _, err := relay.Connect(ctx, relay.Dialer{UDP: dialer(t), TCP: countingTCP(&tcpDials), TCPDelay: time.Second}, cfg, mustID(t)); !errors.Is(err, relay.ErrFingerprint) || errors.Is(err, relay.ErrNoResponse) {
		t.Fatalf("over UDP: err = %v, want ErrFingerprint", err)
	}
	if tcpDials.Load() != 0 {
		t.Fatal("a fingerprint mismatch over UDP must not fall back to TCP")
	}
	if _, err := relay.Connect(ctx, relay.Dialer{UDP: blockUDP, TCPDelay: time.Millisecond}, cfg, mustID(t)); !errors.Is(err, relay.ErrFingerprint) || errors.Is(err, relay.ErrNoResponse) {
		t.Fatalf("over TCP: err = %v, want ErrFingerprint", err)
	}
}

func TestProbeUDP(t *testing.T) {
	r := startRelayAll(t, Limits{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := relay.ProbeUDP(ctx, dialer(t), r.cfg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := r.clientCount(); n != 0 {
		t.Fatalf("a probe must not register; relay has %d clients", n)
	}
	sctx, scancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer scancel()
	start := time.Now()
	if err := relay.ProbeUDP(sctx, blockUDP, r.cfg); err == nil {
		t.Fatal("probe must fail when UDP is blocked")
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("probe must wait for the context")
	}
}

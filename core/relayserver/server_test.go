package relayserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/identity"
	"portide/core/pb"
	"portide/core/relay"
	"portide/core/transport"
	"portide/core/wire"
)

type testRelay struct {
	cfg    relay.Config
	tokens *TokenFile
	stop   func() // 停止中继；startRelay 已经把它注册到 t.Cleanup，测试只在需要主动停止时才调用
}

// startRelay 在回环地址上运行一个中继，返回一份可用的客户端配置。
func startRelay(t *testing.T, limits Limits) *testRelay {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	tokens := OpenTokens(filepath.Join(t.TempDir(), "tokens.json"))
	_, token, err := tokens.Add("测试")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Key: priv, Tokens: tokens, Limits: limits, Logf: t.Logf, Debug: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, pc); close(done) }()
	stop := func() { cancel(); <-done; pc.Close() }
	t.Cleanup(stop)
	return &testRelay{cfg: relay.Config{Addr: pc.LocalAddr().String(), Fingerprint: relay.Fingerprint(pub), Token: token}, tokens: tokens, stop: stop}
}

// dialer 返回一个用独立 UDP 端口拨号的 DialFunc。
func dialer(t *testing.T) relay.DialFunc {
	t.Helper()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	tr := &quic.Transport{Conn: udp}
	t.Cleanup(func() { tr.Close(); udp.Close() })
	return func(ctx context.Context, addr string, tlsConf *tls.Config) (*quic.Conn, error) {
		ua, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		return tr.Dial(ctx, ua, tlsConf, &quic.Config{KeepAlivePeriod: 5 * time.Second})
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

func connect(t *testing.T, r *testRelay, id *identity.Identity) *relay.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := relay.Connect(ctx, relay.Dialer{UDP: dialer(t)}, r.cfg, id)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func nextEvent(t *testing.T, c *relay.Client) relay.Event {
	t.Helper()
	ch := make(chan relay.Event, 1)
	go func() {
		e, err := c.NextEvent()
		if err == nil {
			ch <- e
		}
	}()
	select {
	case e := <-ch:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no relay event")
		return relay.Event{}
	}
}

func TestRegisterRejectsBadTokenAndWrongRelay(t *testing.T) {
	r := startRelay(t, Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bad := r.cfg
	bad.Token = make([]byte, relay.TokenSize)
	if _, err := relay.Connect(ctx, relay.Dialer{UDP: dialer(t)}, bad, mustID(t)); !errors.Is(err, relay.ErrRejected) {
		t.Fatalf("bad token: err = %v, want ErrRejected", err)
	}
	wrong := r.cfg
	wrong.Fingerprint[0] ^= 1
	if _, err := relay.Connect(ctx, relay.Dialer{UDP: dialer(t)}, wrong, mustID(t)); err == nil {
		t.Fatal("a relay with a different fingerprint must be refused")
	}
}

// 签名必须绑定本条连接：用设备自己的密钥、但对别的导出值签名（例如从别的连接上偷来的签名）也会被拒绝。
func TestRegisterRequiresDeviceSignature(t *testing.T) {
	r := startRelay(t, Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialer(t)(ctx, r.cfg.Addr, relay.ClientTLS(r.cfg.Fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "")
	ctrl, _ := conn.OpenStreamSync(ctx)
	victim := mustID(t)
	_ = wire.Write(ctrl, &pb.RelayHello{Token: r.cfg.Token, DeviceKey: victim.Public, Signature: ed25519.Sign(victim.Private, relay.RegisterMessage(make([]byte, relay.ExporterLen)))})
	var w pb.RelayWelcome
	if err := wire.Read(ctrl, &w); err != nil || w.Error != "invalid signature" {
		t.Fatalf("welcome = %+v, %v", &w, err)
	}
}

func TestPresenceEvents(t *testing.T) {
	r := startRelay(t, Limits{})
	laptopID, desktopID := mustID(t), mustID(t)
	laptop := connect(t, r, laptopID)
	if err := laptop.Subscribe([][]byte{relay.RouteID(desktopID.Public)}); err != nil {
		t.Fatal(err)
	}
	desktop := connect(t, r, desktopID)
	if e := nextEvent(t, laptop); !e.Online || !bytes.Equal(e.Route, relay.RouteID(desktopID.Public)) {
		t.Fatalf("online event = %+v", e)
	}
	desktop.Close()
	if e := nextEvent(t, laptop); e.Online {
		t.Fatalf("offline event = %+v", e)
	}
	// 订阅时对方已经在线：立即通知一次。
	desktop = connect(t, r, desktopID)
	_ = nextEvent(t, laptop)
	if err := laptop.Subscribe([][]byte{relay.RouteID(desktopID.Public)}); err != nil {
		t.Fatal(err)
	}
	if e := nextEvent(t, laptop); !e.Online {
		t.Fatalf("event after re-subscribe = %+v", e)
	}
	_ = desktop
}

func TestSessionCarriesPackets(t *testing.T) {
	r := startRelay(t, Limits{})
	laptopID, desktopID := mustID(t), mustID(t)
	laptop := connect(t, r, laptopID)
	desktop := connect(t, r, desktopID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := laptop.Open(ctx, relay.RouteID(mustID(t).Public)); !errors.Is(err, relay.ErrOffline) {
		t.Fatalf("open to an unknown device: err = %v, want ErrOffline", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var in net.PacketConn
	go func() {
		defer wg.Done()
		var err error
		if in, err = desktop.Accept(ctx); err != nil {
			t.Error(err)
		}
	}()
	out, err := laptop.Open(ctx, relay.RouteID(desktopID.Public))
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if in == nil {
		t.FailNow()
	}
	for _, msg := range [][]byte{[]byte("第一个包"), bytes.Repeat([]byte{9}, 1400)} {
		if _, err := out.WriteTo(msg, relay.Addr{}); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		n, addr, err := in.ReadFrom(buf)
		if err != nil || !bytes.Equal(buf[:n], msg) || !relay.IsRelayed(addr) {
			t.Fatalf("ReadFrom = %q %v %v", buf[:n], addr, err)
		}
	}
	// 反方向。
	_, _ = in.WriteTo([]byte("回信"), relay.Addr{})
	buf := make([]byte, 64)
	if n, _, err := out.ReadFrom(buf); err != nil || string(buf[:n]) != "回信" {
		t.Fatalf("reply = %q %v", buf[:n], err)
	}
	// 一端关闭，另一端很快读到错误。
	out.Close()
	_ = in.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := in.ReadFrom(buf); err == nil {
		t.Fatal("the other side must see the session end")
	}
}

func TestSessionLimitPerToken(t *testing.T) {
	r := startRelay(t, Limits{MaxSessionsPerToken: 1})
	laptopID, desktopID := mustID(t), mustID(t)
	laptop := connect(t, r, laptopID)
	desktop := connect(t, r, desktopID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		for {
			if _, err := desktop.Accept(ctx); err != nil {
				return
			}
		}
	}()
	first, err := laptop.Open(ctx, relay.RouteID(desktopID.Public))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.Open(ctx, relay.RouteID(desktopID.Public)); err == nil {
		t.Fatal("the second concurrent session must be refused")
	}
	first.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pc, err := laptop.Open(ctx, relay.RouteID(desktopID.Public))
		if err == nil {
			pc.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session slot was not released: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRevokedTokenDisconnects(t *testing.T) {
	r := startRelay(t, Limits{})
	c := connect(t, r, mustID(t))
	list, _ := r.tokens.List()
	if err := r.tokens.Revoke(list[0].ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(3 * revokeEvery):
		t.Fatal("a client with a revoked token must be disconnected")
	}
}

// 令牌文件读不了时：正在用它的设备几秒内被断开，且服务端日志里出现提示（不含令牌、IP 或路由 ID）。
func TestUnreadableTokenFileDisconnectsAndLogs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受文件权限限制，跳过")
	}
	path := filepath.Join(t.TempDir(), "tokens.json")
	tokens := OpenTokens(path)
	_, token, err := tokens.Add("测试")
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var logMu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	srv := &Server{Key: priv, Tokens: tokens, Logf: logf}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, pc); close(done) }()
	t.Cleanup(func() { cancel(); <-done; pc.Close() })
	r := &testRelay{cfg: relay.Config{Addr: pc.LocalAddr().String(), Fingerprint: relay.Fingerprint(pub), Token: token}}
	c := connect(t, r, mustID(t))

	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	select {
	case <-c.Done():
	case <-time.After(3 * revokeEvery):
		t.Fatal("a client must be disconnected once the token file becomes unreadable")
	}

	deadline := time.Now().Add(3 * revokeEvery)
	for {
		logMu.Lock()
		found := false
		for _, l := range lines {
			if strings.Contains(l, "无法读取令牌文件") {
				found = true
			}
		}
		logMu.Unlock()
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected a log line containing 无法读取令牌文件")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 中继关闭时必须主动通知已登记的设备，而不是让它们等到 60 秒的空闲超时才发现。
func TestShutdownNotifiesClients(t *testing.T) {
	r := startRelay(t, Limits{})
	c := connect(t, r, mustID(t))
	start := time.Now()
	r.stop()
	select {
	case <-c.Done():
		t.Logf("client noticed the shutdown after %v", time.Since(start))
	case <-time.After(2 * time.Second):
		t.Fatal("a client must be disconnected promptly when the relay shuts down")
	}
}

func TestReRegisterReplacesOldConnection(t *testing.T) {
	r := startRelay(t, Limits{})
	id := mustID(t)
	old := connect(t, r, id)
	_ = connect(t, r, id)
	select {
	case <-old.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the old registration must be closed")
	}
}

// 攻击者关注一台在线设备（每次订阅都立即收到上千条上线通知）和新设备，但从不读控制流。
// 同步写通知的实现里，攻击者的流窗口塞满之后，新设备登记时给攻击者发上线通知就会卡住，新设备登记不上。
func TestSlowSubscriberDoesNotBlockOthers(t *testing.T) {
	r := startRelay(t, Limits{})
	onlineID, newcomerID := mustID(t), mustID(t)
	_ = connect(t, r, onlineID)
	attacker := connect(t, r, mustID(t))
	routes := [][]byte{relay.RouteID(newcomerID.Public)}
	for i := 0; i < maxRoutes-1; i++ {
		routes = append(routes, relay.RouteID(onlineID.Public))
	}
	for i := 0; i < 20; i++ {
		if err := attacker.Subscribe(routes); err != nil {
			break // 已经因为太慢被断开
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	newcomer, err := relay.Connect(ctx, relay.Dialer{UDP: dialer(t)}, r.cfg, newcomerID)
	if err != nil {
		t.Fatalf("a slow subscriber must not stall other devices' registration: %v", err)
	}
	newcomer.Close()
	select {
	case <-attacker.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a subscriber whose queue overflows must be disconnected")
	}
}

func TestRateWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &rateWriter{w: &buf, rate: 100 << 10, start: time.Now()}
	start := time.Now()
	for i := 0; i < 5; i++ {
		_, _ = w.Write(make([]byte, 10<<10)) // 共 50 KiB，限速 100 KiB/s
	}
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("50 KiB at 100 KiB/s took only %v", d)
	}
}

// 中继只转发登记设备之间的会话流量，不是配对专用通道：拿着配对 ALPN 拨号经中继转发的会话必须被拒绝。
func TestPairALPNRefusedOverRelay(t *testing.T) {
	r := startRelay(t, Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, b := mustID(t), mustID(t)
	ca, cb := connect(t, r, a), connect(t, r, b)
	res := make(chan error, 1)
	go func() {
		pc, err := cb.Accept(ctx)
		if err != nil {
			res <- err
			return
		}
		_, _, err = transport.AcceptMemberOver(ctx, pc)
		res <- err
	}()
	pc, err := ca.Open(ctx, relay.RouteID(b.Public))
	if err != nil {
		t.Fatal(err)
	}
	tr := &quic.Transport{Conn: pc}
	defer tr.Close()
	dctx, dc := context.WithTimeout(ctx, 2*time.Second)
	defer dc()
	_, derr := tr.Dial(dctx, pc.LocalAddr(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{transport.ALPNPair}, MinVersion: tls.VersionTLS13}, &quic.Config{})
	<-res
	if derr == nil {
		t.Fatal("pairing ALPN must be refused over the relay")
	}
}

// 默认不开 Debug 时，日志里不能出现客户端 IP 或路由 ID 前缀（设计方案 8）；聚合计数那一行是唯一的日志种类。
func TestDefaultLoggingOmitsIPAndRouteID(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	tokens := OpenTokens(filepath.Join(t.TempDir(), "tokens.json"))
	_, token, err := tokens.Add("测试")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	srv := &Server{Key: priv, Tokens: tokens, Logf: logf, Debug: false, StatsEvery: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, pc); close(done) }()
	t.Cleanup(func() { cancel(); <-done; pc.Close() })
	r := &testRelay{cfg: relay.Config{Addr: pc.LocalAddr().String(), Fingerprint: relay.Fingerprint(pub), Token: token}}

	aID, bID := mustID(t), mustID(t)
	a, b := connect(t, r, aID), connect(t, r, bID)
	actx, acancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer acancel()
	accepted := make(chan struct{})
	go func() { _, _ = b.Accept(actx); close(accepted) }()
	out, err := a.Open(actx, relay.RouteID(bID.Public))
	if err != nil {
		t.Fatal(err)
	}
	<-accepted
	out.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(lines)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no stats line was produced")
		}
		time.Sleep(10 * time.Millisecond)
	}

	routePrefix := hex.EncodeToString(relay.RouteID(aID.Public))[:8]
	mu.Lock()
	defer mu.Unlock()
	for _, l := range lines {
		if !strings.Contains(l, "在线设备") {
			t.Fatalf("unexpected non-stats log line with Debug=false: %q", l)
		}
		if strings.Contains(l, "127.0.0.1") || strings.Contains(l, routePrefix) {
			t.Fatalf("log line leaks IP or route ID: %q", l)
		}
	}
}

// 中继在登记回复里告诉设备它看到的地址：本机用哪个 UDP 端口连的中继，看到的就是哪个。
func TestWelcomeReportsObservedAddr(t *testing.T) {
	r := startRelay(t, Limits{})
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	tr := &quic.Transport{Conn: udp}
	t.Cleanup(func() { tr.Close(); udp.Close() })
	dial := func(ctx context.Context, addr string, tlsConf *tls.Config) (*quic.Conn, error) {
		ua, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		return tr.Dial(ctx, ua, tlsConf, &quic.Config{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := relay.Connect(ctx, relay.Dialer{UDP: dial}, r.cfg, mustID(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, want := c.ObservedAddr(), udp.LocalAddr().String(); got != want {
		t.Fatalf("observed addr = %q, want %q", got, want)
	}
}

// tcpRelay 是同时监听 UDP 和 TCP 的测试中继。
type tcpRelay struct {
	*testRelay
	srv     *Server
	tcpAddr string
	logs    func() []string
}

// startRelayAll 在回环地址上同时监听 UDP 和 TCP；setup 可在启动前调整 Server（例如改小未导出的限制）。
func startRelayAll(t *testing.T, limits Limits, setup func(*Server)) *tcpRelay {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	tokens := OpenTokens(filepath.Join(t.TempDir(), "tokens.json"))
	_, token, err := tokens.Add("测试")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
		t.Log(line)
	}
	srv := &Server{Key: priv, Tokens: tokens, Limits: limits, Logf: logf, Debug: true}
	if setup != nil {
		setup(srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.ServeAll(ctx, pc, ln); close(done) }()
	stop := func() { cancel(); <-done; pc.Close(); ln.Close() }
	t.Cleanup(stop)
	return &tcpRelay{
		testRelay: &testRelay{cfg: relay.Config{Addr: pc.LocalAddr().String(), Fingerprint: relay.Fingerprint(pub), Token: token}, tokens: tokens, stop: stop},
		srv:       srv,
		tcpAddr:   ln.Addr().String(),
		logs: func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), lines...)
		},
	}
}

// dialQUICOverTCP 经 TCP 通道拨通中继的 QUIC（只完成握手，不登记）；返回的 cleanup 关闭连接、Transport 和 TCP。
func dialQUICOverTCP(ctx context.Context, r *tcpRelay) (*quic.Conn, func(), error) {
	pc, err := relay.DialTCP(ctx, (&net.Dialer{}).DialContext, r.tcpAddr, r.cfg.Fingerprint)
	if err != nil {
		return nil, nil, err
	}
	tr := &quic.Transport{Conn: pc}
	conn, err := tr.Dial(ctx, pc.(interface{ RemoteAddr() net.Addr }).RemoteAddr(), relay.ClientTLS(r.cfg.Fingerprint), &quic.Config{KeepAlivePeriod: 5 * time.Second})
	if err != nil {
		tr.Close()
		pc.Close()
		return nil, nil, err
	}
	return conn, func() { _ = conn.CloseWithError(0, ""); tr.Close(); pc.Close() }, nil
}

// connectTCPErr 经 TCP 通道连接中继并登记（UDP 被屏蔽，客户端只能走 TCP）。
func connectTCPErr(t *testing.T, r *tcpRelay, id *identity.Identity) (*relay.Client, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// UDP 立即失败（不像 blockUDP 那样挂到超时），TCP 一失败 Connect 就返回
	failUDP := func(context.Context, string, *tls.Config) (*quic.Conn, error) { return nil, errors.New("udp blocked") }
	c, err := relay.Connect(ctx, relay.Dialer{UDP: failUDP, TCPDelay: time.Nanosecond}, relayCfg(t, r), id)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { c.Close() })
	return c, nil
}

// registerRaw 在已经握手的 conn 上按协议登记，和 relay.Connect 的登记一步相同；用于需要单独控制 QUIC 和 TCP 的测试。
func registerRaw(ctx context.Context, conn *quic.Conn, token []byte, id *identity.Identity) error {
	ctrl, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	st := conn.ConnectionState()
	exp, err := st.TLS.ExportKeyingMaterial(relay.ExporterLabel, nil, relay.ExporterLen)
	if err != nil {
		return err
	}
	hello := &pb.RelayHello{Token: token, DeviceKey: id.Public, Signature: ed25519.Sign(id.Private, relay.RegisterMessage(exp))}
	if err := wire.Write(ctrl, hello); err != nil {
		return err
	}
	var w pb.RelayWelcome
	if err := wire.ReadMax(ctrl, &w, 1024); err != nil {
		return err
	}
	if w.Error != "" {
		return fmt.Errorf("rejected: %s", w.Error)
	}
	return nil
}

func connectTCP(t *testing.T, r *tcpRelay, id *identity.Identity) *relay.Client {
	t.Helper()
	c, err := connectTCPErr(t, r, id)
	if err != nil {
		t.Fatalf("connect over TCP: %v", err)
	}
	return c
}

func eventually(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServeUDPAndTCPClientsMeet(t *testing.T) {
	r := startRelayAll(t, Limits{}, nil)
	aID, bID := mustID(t), mustID(t)
	a := connect(t, r.testRelay, aID)
	b := connectTCP(t, r, bID)
	if err := a.Subscribe([][]byte{relay.RouteID(bID.Public)}); err != nil {
		t.Fatal(err)
	}
	if e := nextEvent(t, a); !e.Online || !bytes.Equal(e.Route, relay.RouteID(bID.Public)) {
		t.Fatalf("online event = %+v", e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	accepted := make(chan net.PacketConn, 1)
	go func() {
		in, err := b.Accept(ctx)
		if err != nil {
			t.Error(err)
		}
		accepted <- in
	}()
	out, err := a.Open(ctx, relay.RouteID(bID.Public))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	in := <-accepted
	if in == nil {
		t.FailNow()
	}
	defer in.Close()
	buf := make([]byte, 2048)
	if _, err := out.WriteTo([]byte("经 UDP 发出"), relay.Addr{}); err != nil {
		t.Fatal(err)
	}
	_ = in.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, _, err := in.ReadFrom(buf); err != nil || string(buf[:n]) != "经 UDP 发出" {
		t.Fatalf("B got %q %v", buf[:n], err)
	}
	if _, err := in.WriteTo([]byte("经 TCP 回信"), relay.Addr{}); err != nil {
		t.Fatal(err)
	}
	_ = out.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, _, err := out.ReadFrom(buf); err != nil || string(buf[:n]) != "经 TCP 回信" {
		t.Fatalf("A got %q %v", buf[:n], err)
	}
	// 也能反过来：B（经 TCP）开会话到 A。
	go func() {
		in, err := a.Accept(ctx)
		if err == nil {
			accepted <- in
		}
	}()
	back, err := b.Open(ctx, relay.RouteID(aID.Public))
	if err != nil {
		t.Fatal(err)
	}
	back.Close()
	(<-accepted).Close()
}

func TestTCPClientsShareLimits(t *testing.T) {
	r := startRelayAll(t, Limits{MaxClients: 1}, nil)
	_ = connect(t, r.testRelay, mustID(t))
	if _, err := connectTCPErr(t, r, mustID(t)); !errors.Is(err, relay.ErrRejected) {
		t.Fatalf("second device over TCP: err = %v, want ErrRejected", err)
	}
}

// 有人开很多 TCP 连接却不完成握手：未完成的连接数有上限，超出的立刻关掉；空连接到点被回收；
// 腾出名额后正常设备能经 TCP 登记（Review Focus 2）。
func TestTCPPendingCap(t *testing.T) {
	const hs = 1500 * time.Millisecond
	r := startRelayAll(t, Limits{}, func(s *Server) { s.tcpMaxPending = 2; s.hsTimeout = hs })
	idle := func() net.Conn {
		c, err := net.Dial("tcp", r.tcpAddr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	// closedWithin 报告 c 是否在 d 内被对端关闭。
	closedWithin := func(c net.Conn, d time.Duration) bool {
		_ = c.SetReadDeadline(time.Now().Add(d))
		_, err := c.Read(make([]byte, 1))
		var ne net.Error
		return err != nil && !(errors.As(err, &ne) && ne.Timeout())
	}
	start := time.Now()
	i1, i2 := idle(), idle()
	time.Sleep(100 * time.Millisecond) // 让服务端先把前两条计入
	i3 := idle()
	if !closedWithin(i3, hs/2) {
		t.Fatal("a connection beyond the pending cap must be closed at once")
	}
	if _, err := connectTCPErr(t, r, mustID(t)); err == nil {
		t.Fatal("registration must fail while the pending slots are taken")
	}
	// 关掉一条空连接，腾出名额：正常设备能经 TCP 登记。
	i1.Close()
	var c *relay.Client
	eventually(t, hs/2, "a device must register over TCP once a slot frees up", func() bool {
		var err error
		c, err = connectTCPErr(t, r, mustID(t))
		return err == nil
	})
	_ = c
	if time.Since(start) >= hs {
		t.Fatalf("test too slow (%v) to tell the cap from the handshake timeout", time.Since(start))
	}
	// 另一条空连接不完成 TLS：握手超时后被关闭。
	if closedWithin(i2, 50*time.Millisecond) {
		t.Fatal("an idle connection must not be closed before the handshake timeout")
	}
	if !closedWithin(i2, 2*hs) {
		t.Fatal("a connection that never finishes TLS must be closed after the handshake timeout")
	}
}

// 完成了 QUIC 握手（不再算"未完成"）却从不登记的连接：登记超时后中继关掉 QUIC 连接，连同它的 TCP 连接。
// 它们在等待登记期间不占未完成的名额。
func TestTCPEstablishedButUnregisteredIsClosed(t *testing.T) {
	const hs = 1500 * time.Millisecond
	r := startRelayAll(t, Limits{}, func(s *Server) { s.tcpMaxPending = 2; s.hsTimeout = hs })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var conns []*quic.Conn
	for i := 0; i < 2; i++ {
		conn, cleanup, err := dialQUICOverTCP(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		conns = append(conns, conn)
	}
	// 这两条已经 Established，不占名额：第三台正常设备能登记。
	eventually(t, hs/2, "QUIC-established connections must not hold pending slots", func() bool {
		_, err := connectTCPErr(t, r, mustID(t))
		return err == nil
	})
	for _, conn := range conns {
		select {
		case <-conn.Context().Done():
		case <-time.After(3 * hs):
			t.Fatal("a connection that never registers must be closed")
		}
	}
	tcpCount := func() int {
		r.srv.mu.Lock()
		p := r.srv.tcpConn
		r.srv.mu.Unlock()
		return p.Count()
	}
	eventually(t, 2*time.Second, "the TCP connections of unregistered QUIC connections must be closed", func() bool { return tcpCount() == 1 })
}

// 经 TCP 登记的设备结束后（这里是设备自己断开 QUIC），中继也关掉它的 TCP 连接。
func TestTCPConnClosedWhenClientLeaves(t *testing.T) {
	r := startRelayAll(t, Limits{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, cleanup, err := dialQUICOverTCP(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := registerRaw(ctx, conn, r.cfg.Token, mustID(t)); err != nil {
		t.Fatal(err)
	}
	r.srv.mu.Lock()
	p := r.srv.tcpConn
	r.srv.mu.Unlock()
	if p.Count() != 1 {
		t.Fatalf("Count = %d", p.Count())
	}
	_ = conn.CloseWithError(0, "") // 只关 QUIC，不关 TCP
	eventually(t, 5*time.Second, "the relay must close the TCP connection of a departed client", func() bool { return p.Count() == 0 })
}

func TestServeStatsCountsTCP(t *testing.T) {
	r := startRelayAll(t, Limits{}, func(s *Server) { s.StatsEvery = 50 * time.Millisecond })
	_ = connect(t, r.testRelay, mustID(t))
	_ = connectTCP(t, r, mustID(t))
	eventually(t, 3*time.Second, "stats must count TCP clients", func() bool {
		for _, l := range r.logs() {
			if strings.Contains(l, "在线设备 2（其中经 TCP 1）") {
				return true
			}
		}
		return false
	})
	found := false
	for _, l := range r.logs() {
		if strings.HasPrefix(l, "登记：") && strings.HasSuffix(l, "（TCP）") {
			found = true
		}
	}
	if !found {
		t.Fatal("the debug registration log must end with （TCP） for a TCP client")
	}
}

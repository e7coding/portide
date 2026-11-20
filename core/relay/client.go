package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"portide/core/identity"
	"portide/core/pb"
	"portide/core/wire"
)

var (
	// ErrRejected 表示中继拒绝了登记（令牌无效、签名不对、中继已满）。
	ErrRejected = errors.New("relay: registration rejected")
	// ErrOffline 表示要连接的设备此刻没有在中继上登记。
	ErrOffline = errors.New("relay: device is not online at the relay")
)

// openTimeout 是建立一次会话（发 RelayOpen 到收到回复）的时限。
const openTimeout = 10 * time.Second

// DialFunc 用指定的 TLS 配置连接中继。守护进程用自己的 QUIC 端点拨出，这样中继看到的就是本机监听端口的公网地址。
type DialFunc func(ctx context.Context, addr string, tlsConf *tls.Config) (*quic.Conn, error)

// Event 是中继通知的一次上线或下线。
type Event struct {
	Route  []byte
	Online bool
}

// Client 是设备到中继的一条连接（设计方案 4.2 第 1 条"在线登记"）。
// Client 自己不启动常驻 goroutine：调用方循环调用 NextEvent 读通知、调用 Accept 接收会话。
type Client struct {
	conn      *quic.Conn
	ctrl      *quic.Stream
	wmu       sync.Mutex
	observed  string // 中继看到的本机地址（见 ObservedAddr）
	addr      string // 中继的地址（配置里的），填进会话的 Addr
	transport string // "udp" 或 "tcp"

	// 经 TCP 时：这条 QUIC 连接专用的 TCP 虚拟 PacketConn，以及关掉它和专用 quic.Transport 的函数（可重复调用）
	pc       net.PacketConn
	closeTCP func()
}

// TCPDelay 是 UDP 开始拨号后，多久还没完成握手就同时拨 TCP 备用通道（设计 D5）。
const TCPDelay = 3 * time.Second

// ErrNoResponse 表示 UDP 和 TCP 两条路都没能和中继完成握手（多半是防火墙或安全组没放行）。
// 返回的错误同时包装了两条路各自的原始错误。
var ErrNoResponse = errors.New("relay: no response from the relay over UDP or TCP")

// noResponseError 是两条路都失败时的错误：errors.Is 能认出 ErrNoResponse 和两条路的原始错误。
type noResponseError struct{ udp, tcp error }

func (e *noResponseError) Error() string {
	s := ErrNoResponse.Error()
	if e.udp != nil {
		s += "; udp: " + e.udp.Error()
	}
	if e.tcp != nil {
		s += "; tcp: " + e.tcp.Error()
	}
	return s
}

func (e *noResponseError) Unwrap() []error {
	errs := []error{ErrNoResponse}
	for _, err := range []error{e.udp, e.tcp} {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// tcpQUICConfig 是经 TCP 通道连中继时的 QUIC 参数，与守护进程拨中继（transport.relayQUICConfig）一致。
var tcpQUICConfig = &quic.Config{
	MaxIdleTimeout:       60 * time.Second,
	KeepAlivePeriod:      15 * time.Second,
	HandshakeIdleTimeout: 10 * time.Second,
}

// Dialer 说明怎样连中继：先走 UDP，TCPDelay 之后还没握手成功就同时走 TCP（设计 D5）。
type Dialer struct {
	UDP DialFunc // 守护进程的 UDP 端点（与成员连接共用，只关 QUIC 连接，不关端点）
	// TCP 拨 TCP 连接；为 nil 时用 net.Dialer
	TCP func(ctx context.Context, network, addr string) (net.Conn, error)
	// TCPDelay 为 0 时用常量 TCPDelay
	TCPDelay time.Duration
}

// dialed 是一条路的握手结果。
type dialed struct {
	conn     *quic.Conn
	tcp      bool
	pc       net.PacketConn // 经 TCP 时
	closeTCP func()         // 经 TCP 时：关掉专用 Transport 和 TCP 连接
	err      error
}

// close 关掉握手成功但不用的连接（还没登记）。
func (r dialed) close() {
	if r.conn != nil {
		_ = r.conn.CloseWithError(0, "")
	}
	if r.closeTCP != nil {
		r.closeTCP()
	}
}

// Connect 连接中继并登记本机（令牌 + 设备密钥对 TLS 导出值的签名）。
// 先经 UDP 握手；TCPDelay 内没完成就同时经 TCP 握手，先完成的那条用来登记，另一条在登记之前就取消或关掉。
// 两条都失败时返回的错误满足 errors.Is(err, ErrNoResponse)；指纹不对时返回 ErrFingerprint。
func Connect(ctx context.Context, d Dialer, cfg Config, self *identity.Identity) (*Client, error) {
	r, err := d.handshake(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if r.closeTCP != nil {
		// QUIC 连接无论因为什么断开，都连带关掉它专用的 Transport 和 TCP 连接
		context.AfterFunc(r.conn.Context(), r.closeTCP)
	}
	c, err := register(ctx, r.conn, cfg.Token, self)
	if err != nil {
		r.close()
		return nil, err
	}
	c.addr = cfg.Addr
	c.transport = "udp"
	if r.tcp {
		c.transport = "tcp"
		c.observed = "" // 那是 TCP 连接的源端口，和本机 UDP 监听端口无关，不能拿去打洞（设计 D7）
		c.pc, c.closeTCP = r.pc, r.closeTCP
	}
	return c, nil
}

// handshake 按 D5 让 UDP 和 TCP 赛跑，只返回第一条握手成功的 QUIC 连接；返回前另一条已经结束
// （取消了拨号，或者握手成功了也已关掉），不留 goroutine，也不留半开的连接。
func (d Dialer) handshake(ctx context.Context, cfg Config) (dialed, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan dialed, 2)
	pending := 1
	go func() {
		conn, err := d.UDP(ctx, cfg.Addr, ClientTLS(cfg.Fingerprint))
		results <- dialed{conn: conn, err: err}
	}()

	delay := d.TCPDelay
	if delay <= 0 {
		delay = TCPDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	tcpStarted := false
	startTCP := func() {
		tcpStarted = true
		pending++
		go func() { results <- d.dialTCP(ctx, cfg) }()
	}
	// finish 取消还在拨的那条并等它结束；它要是也握手成功了，立刻关掉（这时还没登记）。
	finish := func() {
		cancel()
		for ; pending > 0; pending-- {
			if l := <-results; l.err == nil {
				l.close()
			}
		}
	}

	var udpErr, tcpErr error
	for pending > 0 {
		var tick <-chan time.Time
		if !tcpStarted {
			tick = timer.C
		}
		select {
		case <-tick:
			startTCP()
		case r := <-results:
			pending--
			if r.err == nil {
				finish()
				return r, nil
			}
			if r.tcp {
				tcpErr = r.err
			} else {
				udpErr = r.err
			}
			if errors.Is(r.err, ErrFingerprint) {
				// 明确的错误：对面不是这个中继，换条路也一样
				finish()
				return dialed{}, r.err
			}
			if !tcpStarted && ctx.Err() == nil {
				startTCP() // UDP 很快就失败了（例如 ICMP 不可达）：不必等满 TCPDelay
			}
		}
	}
	return dialed{}, &noResponseError{udp: udpErr, tcp: tcpErr}
}

// dialTCP 经 TCP 备用通道握手：TCP → 外层 TLS → 在这条连接上用一个单独的 quic.Transport 拨中继 QUIC。
func (d Dialer) dialTCP(ctx context.Context, cfg Config) dialed {
	dial := d.TCP
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	pc, err := DialTCP(ctx, dial, cfg.TCPAddr(), cfg.Fingerprint)
	if err != nil {
		return dialed{tcp: true, err: err}
	}
	tr := &quic.Transport{Conn: pc}
	closeTCP := sync.OnceFunc(func() {
		_ = tr.Close()
		_ = pc.Close()
	})
	conn, err := tr.Dial(ctx, pc.(*tcpClientConn).RemoteAddr(), ClientTLS(cfg.Fingerprint), tcpQUICConfig)
	if err != nil {
		closeTCP()
		return dialed{tcp: true, err: err}
	}
	return dialed{conn: conn, tcp: true, pc: pc, closeTCP: closeTCP}
}

// ProbeUDP 只经 UDP 和中继做一次 QUIC 握手，成功就立刻关掉，不登记（设计 D6：经 TCP 连着时探测 UDP 是否恢复）。
func ProbeUDP(ctx context.Context, dial DialFunc, cfg Config) error {
	conn, err := dial(ctx, cfg.Addr, ClientTLS(cfg.Fingerprint))
	if err != nil {
		return err
	}
	return conn.CloseWithError(0, "")
}

func register(ctx context.Context, conn *quic.Conn, token []byte, self *identity.Identity) (*Client, error) {
	ctrl, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = ctrl.SetDeadline(dl)
	}
	st := conn.ConnectionState()
	exp, err := st.TLS.ExportKeyingMaterial(ExporterLabel, nil, ExporterLen)
	if err != nil {
		return nil, err
	}
	hello := &pb.RelayHello{
		Token:     token,
		DeviceKey: self.Public,
		Signature: ed25519.Sign(self.Private, RegisterMessage(exp)),
	}
	if err := wire.Write(ctrl, hello); err != nil {
		return nil, err
	}
	var w pb.RelayWelcome
	if err := wire.ReadMax(ctrl, &w, 1024); err != nil {
		return nil, fmt.Errorf("relay: read welcome: %w", err)
	}
	if w.Error != "" {
		return nil, fmt.Errorf("%w: %s", ErrRejected, w.Error)
	}
	_ = ctrl.SetDeadline(time.Time{})
	return &Client{conn: conn, ctrl: ctrl, observed: w.ObservedAddr}, nil
}

// ObservedAddr 返回中继看到的本机地址（host:port）。本机在 NAT 之后时，这就是 NAT 给本机监听端口分配的公网地址；
// 中继没有提供、或者经 TCP 连着时为空。
func (c *Client) ObservedAddr() string { return c.observed }

// Transport 返回这条连接走的是 "udp" 还是 "tcp"（备用通道）。
func (c *Client) Transport() string { return c.transport }

// Subscribe 整体替换要关注的设备（路由 ID）：它们上线、下线时中继发通知；已经在线的立即通知一次。
func (c *Client) Subscribe(routes [][]byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return wire.Write(c.ctrl, &pb.RelaySubscribe{Routes: routes})
}

// NextEvent 等待下一条上线 / 下线通知。连接断开时返回错误。同一时间只能有一个调用方。
func (c *Client) NextEvent() (Event, error) {
	var e pb.RelayEvent
	if err := wire.ReadMax(c.ctrl, &e, 1024); err != nil {
		return Event{}, err
	}
	return Event{Route: e.Route, Online: e.Online}, nil
}

// Open 经中继连接路由 ID 为 route 的设备，返回承载端到端连接的虚拟 UDP。对方不在线时返回 ErrOffline。
func (c *Client) Open(ctx context.Context, route []byte) (net.PacketConn, error) {
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	str, err := c.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	dl, _ := ctx.Deadline()
	_ = str.SetDeadline(dl)
	if err := wire.Write(str, &pb.RelayOpen{Route: route}); err != nil {
		str.CancelRead(0)
		_ = str.Close()
		return nil, err
	}
	var r pb.RelayOpenReply
	if err := wire.ReadMax(str, &r, 1024); err != nil {
		str.CancelRead(0)
		_ = str.Close()
		return nil, err
	}
	if r.Error != "" {
		str.CancelRead(0)
		_ = str.Close()
		if r.Error == OpenOffline {
			return nil, ErrOffline
		}
		return nil, fmt.Errorf("relay: %s", r.Error)
	}
	_ = str.SetDeadline(time.Time{})
	return newPacketConn(str, c.addr), nil
}

// Accept 等待别的设备经中继连过来，返回承载端到端连接的虚拟 UDP。
func (c *Client) Accept(ctx context.Context) (net.PacketConn, error) {
	for {
		str, err := c.conn.AcceptStream(ctx)
		if err != nil {
			return nil, err
		}
		_ = str.SetReadDeadline(time.Now().Add(openTimeout))
		var in pb.RelayIncoming
		if err := wire.ReadMax(str, &in, 64); err != nil {
			str.CancelRead(0)
			_ = str.Close()
			continue
		}
		_ = str.SetReadDeadline(time.Time{})
		return newPacketConn(str, c.addr), nil
	}
}

// Done 在到中继的连接断开时关闭。
func (c *Client) Done() <-chan struct{} { return c.conn.Context().Done() }

// Close 断开中继；经 TCP 时一起关掉 TCP 连接。
func (c *Client) Close() error {
	err := c.conn.CloseWithError(0, "")
	if c.closeTCP != nil {
		c.closeTCP()
	}
	return err
}

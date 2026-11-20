// Package relayserver 是中继 portide-relay 的服务端（设计方案 4.2、8）：设备在线登记、上线通知、会话转发、访问令牌。
// 协议和客户端在 relay 包里。本包使用 AGPL-3.0，客户端不得导入它（见 CONTRIBUTING.md）。
package relayserver

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"portide/core/i18n"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	"portide/core/pb"
	"portide/core/relay"
	"portide/core/wire"
)

const (
	handshakeTimeout = 10 * time.Second
	maxRoutes        = 1024 // 一次订阅最多关注的设备数
	revokeEvery      = 5 * time.Second
	eventQueue       = 256 // 每台设备待发的上线/下线通知上限；超过说明它不读，断开它
	maxPendingTCP    = 256 // 还没完成 QUIC 握手的 TCP 连接上限（含正在做 TLS 握手的）
	// codeClosed 是中继主动断开客户端时的错误码（令牌被吊销、同一设备在别处重新登记等）。
	codeClosed = quic.ApplicationErrorCode(1)
)

// TokenChecker 校验访问令牌。TokenFile 实现了它。
type TokenChecker interface {
	Check(token []byte) (id string, ok bool)
	Valid(id string) bool
}

// tokenHealth 是 TokenChecker 的可选扩展：报告最近一次读取令牌文件是否出错。TokenFile 实现了它。
type tokenHealth interface{ Err() error }

// Limits 是中继的限制配置（设计方案 8）。零值表示用默认值。
type Limits struct {
	MaxClients          int   // 同时登记的设备数上限，默认 1000
	MaxSessionsPerToken int   // 每个令牌同时进行的会话数上限，默认 64
	SessionRate         int64 // 每个会话每个方向的字节/秒上限；0 表示不限
}

// Server 是中继服务端。
type Server struct {
	Key    ed25519.PrivateKey
	Tokens TokenChecker
	Limits Limits
	// Logf 记录日志。默认只记聚合计数，不记 IP 和路由 ID（设计方案 8）；Debug 为 true 时记每次登记和会话。
	Logf       func(format string, args ...any)
	Debug      bool
	StatsEvery time.Duration // 聚合计数的间隔，默认 10 分钟

	mu       sync.Mutex
	clients  map[string]*client              // 路由 ID（十六进制）→ 登记的设备
	subs     map[string]map[*client]struct{} // 路由 ID → 关注它的设备
	perToken map[string]int                  // 令牌编号 → 进行中的会话数

	sessions atomic.Int64 // 进行中的会话
	total    atomic.Int64 // 累计会话
	bytes    atomic.Int64 // 累计转发字节
	wg       sync.WaitGroup

	lastTokenErr string // housekeeping 上次记录的令牌文件错误文本；只在 housekeeping 所在的 goroutine 里访问

	// 可在测试里改小；零值表示用默认值
	tcpMaxPending int           // 默认 maxPendingTCP
	hsTimeout     time.Duration // 握手和登记的时限，默认 handshakeTimeout

	tcpConn *tcpPacketConn // 当前的 TCP 通道（没有监听 TCP 时为 nil）；受 mu 保护
}

type client struct {
	conn    *quic.Conn
	ctrl    *quic.Stream
	wmu     sync.Mutex
	route   string
	tokenID string
	subs    []string
	events  chan *pb.RelayEvent
	closed  bool
	viaTCP  bool // 经 TCP 通道登记的
}

func (s *Server) limits() Limits {
	l := s.Limits
	if l.MaxClients <= 0 {
		l.MaxClients = 1000
	}
	if l.MaxSessionsPerToken <= 0 {
		l.MaxSessionsPerToken = 64
	}
	return l
}

func (s *Server) handshakeTimeout() time.Duration {
	if s.hsTimeout > 0 {
		return s.hsTimeout
	}
	return handshakeTimeout
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Server) debugf(format string, args ...any) {
	if s.Debug {
		s.logf(format, args...)
	}
}

// enqueueLocked 把一条通知按顺序放进 c 的队列，不阻塞；队列满了说明对方不读控制流，关闭队列（随后断开它）。调用方持有 s.mu。
func (s *Server) enqueueLocked(c *client, route string, online bool) {
	if c.closed {
		return
	}
	raw, _ := hex.DecodeString(route)
	select {
	case c.events <- &pb.RelayEvent{Route: raw, Online: online}:
	default:
		s.closeEventsLocked(c)
	}
}

// closeEventsLocked 关闭 c 的通知队列：writeEvents 发完已排队的通知后退出并断开连接。调用方持有 s.mu。
func (s *Server) closeEventsLocked(c *client) {
	if !c.closed {
		c.closed = true
		close(c.events)
	}
}

// writeEvents 按顺序把 c 队列里的通知写到它的控制流（每台设备一个，写得慢只影响它自己）；队列关闭后断开连接。
func (s *Server) writeEvents(c *client) {
	for e := range c.events {
		if err := c.send(e); err != nil {
			_ = c.conn.CloseWithError(0, "")
		}
	}
	_ = c.conn.CloseWithError(codeClosed, "")
}

// Serve 在 pc 上提供中继服务，直到 ctx 结束；返回前断开所有设备并等待后台任务结束。
func (s *Server) Serve(ctx context.Context, pc net.PacketConn) error {
	return s.ServeAll(ctx, pc, nil)
}

// ServeAll 同时在 UDP 的 pc 和 TCP 的 tcp 上提供中继服务（tcp 为 nil 时只服务 UDP），直到 ctx 结束或任一边出错。
// 两边各用一个 quic.Transport，共用设备登记、订阅和限制。tcp 归 ServeAll 所有，返回时被关闭；pc 由调用方关闭。
func (s *Server) ServeAll(ctx context.Context, pc net.PacketConn, tcp net.Listener) error {
	var tpc *tcpPacketConn
	if tcp != nil {
		defer tcp.Close() // 证书出错等提前返回时也关掉；之后由 tpc.Close 关
	}
	cert, err := relay.ServerCertificate(s.Key)
	if err != nil {
		return err
	}
	if tcp != nil {
		maxPending := s.tcpMaxPending
		if maxPending <= 0 {
			maxPending = maxPendingTCP
		}
		tpc = newTCPPacketConnTimeout(tcp, &tls.Config{
			Certificates:           []tls.Certificate{cert},
			NextProtos:             []string{relay.ALPNTCP},
			MinVersion:             tls.VersionTLS13,
			SessionTicketsDisabled: true,
		}, maxPending, s.handshakeTimeout())
	}
	s.mu.Lock()
	s.clients = map[string]*client{}
	s.subs = map[string]map[*client]struct{}{}
	s.perToken = map[string]int{}
	s.tcpConn = tpc
	s.mu.Unlock()
	tlsConf := &tls.Config{
		Certificates:           []tls.Certificate{cert},
		NextProtos:             []string{relay.ALPN},
		MinVersion:             tls.VersionTLS13,
		SessionTicketsDisabled: true,
	}
	quicConf := &quic.Config{
		MaxIdleTimeout:        60 * time.Second,
		KeepAlivePeriod:       15 * time.Second,
		HandshakeIdleTimeout:  s.handshakeTimeout(),
		MaxIncomingStreams:    1000,
		MaxIncomingUniStreams: -1,
	}
	ctx, cancel := context.WithCancel(ctx)
	var trs []*quic.Transport
	defer func() {
		cancel()
		s.mu.Lock()
		closing := make([]*client, 0, len(s.clients))
		for _, c := range s.clients {
			closing = append(closing, c)
		}
		s.mu.Unlock()
		for _, c := range closing {
			_ = c.conn.CloseWithError(codeClosed, "relay shutting down")
		}
		for _, tr := range trs {
			_ = tr.Close()
		}
		if tpc != nil {
			_ = tpc.Close()
		}
		s.wg.Wait()
	}()
	// UDP 的 pc 原样交给 quic，不包装
	trU := &quic.Transport{Conn: pc}
	trs = append(trs, trU)
	lnU, err := trU.Listen(tlsConf, quicConf)
	if err != nil {
		return err
	}
	var lnT *quic.Listener
	if tpc != nil {
		trT := &quic.Transport{Conn: tpc}
		trs = append(trs, trT)
		if lnT, err = trT.Listen(tlsConf, quicConf); err != nil {
			return err
		}
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.housekeeping(ctx) }()
	errc := make(chan error, 2)
	accept := func(ln *quic.Listener, tpc *tcpPacketConn) {
		for {
			conn, err := ln.Accept(ctx)
			if err != nil {
				errc <- err
				return
			}
			if tpc != nil {
				tpc.Established(conn.RemoteAddr()) // QUIC 握手完成：这条 TCP 连接不再占"未完成"的名额
			}
			s.wg.Add(1)
			go func() { defer s.wg.Done(); s.serveConn(ctx, conn, tpc) }()
		}
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); accept(lnU, nil) }()
	if lnT != nil {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); accept(lnT, tpc) }()
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
}

// CloseTCPConns 断开当前所有经 TCP 通道的连接（不关监听），相当于这些设备的网络断了一下。
// 给测试用（例如 core/node 测试经 TCP 连着时连接被掐断后的重连）；没有监听 TCP 时什么也不做。
func (s *Server) CloseTCPConns() {
	s.mu.Lock()
	tpc := s.tcpConn
	s.mu.Unlock()
	if tpc != nil {
		tpc.closeAll()
	}
}

// housekeeping 定期断开令牌已被吊销的设备，并记录聚合计数。
func (s *Server) housekeeping(ctx context.Context) {
	every := s.StatsEvery
	if every <= 0 {
		every = 10 * time.Minute
	}
	revoke := time.NewTicker(revokeEvery)
	defer revoke.Stop()
	stats := time.NewTicker(every)
	defer stats.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-revoke.C:
			s.mu.Lock()
			byToken := map[string][]*client{}
			for _, c := range s.clients {
				byToken[c.tokenID] = append(byToken[c.tokenID], c)
			}
			s.mu.Unlock()
			for tokenID, clients := range byToken {
				if !s.Tokens.Valid(tokenID) {
					for _, c := range clients {
						_ = c.conn.CloseWithError(codeClosed, "token revoked")
					}
				}
			}
			if th, ok := s.Tokens.(tokenHealth); ok {
				if err := th.Err(); err != nil {
					if err.Error() != s.lastTokenErr {
						s.lastTokenErr = err.Error()
						s.logf(i18n.T("无法读取令牌文件：%v（新设备无法登记，已登记的设备会被断开；检查文件的所有者和权限）"), err)
					}
				} else if s.lastTokenErr != "" {
					s.lastTokenErr = ""
					s.logf(i18n.T("令牌文件恢复可读"))
				}
			}
		case <-stats.C:
			s.mu.Lock()
			online, viaTCP := len(s.clients), 0
			for _, c := range s.clients {
				if c.viaTCP {
					viaTCP++
				}
			}
			s.mu.Unlock()
			s.logf(i18n.T("在线设备 %d（其中经 TCP %d），进行中的会话 %d，累计会话 %d，累计转发 %.1f MB"),
				online, viaTCP, s.sessions.Load(), s.total.Load(), float64(s.bytes.Load())/1e6)
		}
	}
}

// serveConn 处理一条 QUIC 连接：登记，然后接收会话请求。tpc 非 nil 表示这条连接经 TCP 通道而来。
func (s *Server) serveConn(ctx context.Context, conn *quic.Conn, tpc *tcpPacketConn) {
	if tpc != nil {
		// 不管因为什么结束（登记失败或超时、空闲超时、设备离开、令牌吊销、中继关闭），都关掉它的 TCP 连接，
		// 免得对端不关时一直占着
		defer tpc.CloseAddr(conn.RemoteAddr())
	}
	hctx, cancel := context.WithTimeout(ctx, s.handshakeTimeout())
	ctrl, err := conn.AcceptStream(hctx)
	cancel()
	if err != nil {
		_ = conn.CloseWithError(0, "")
		return
	}
	_ = ctrl.SetDeadline(time.Now().Add(s.handshakeTimeout()))
	var h pb.RelayHello
	if err := wire.ReadMax(ctrl, &h, 1024); err != nil {
		_ = conn.CloseWithError(0, "")
		return
	}
	c, why := s.admit(conn, ctrl, &h, tpc != nil)
	if why != "" {
		s.debugf(i18n.T("拒绝登记（%s）：%s"), conn.RemoteAddr(), why)
		_ = wire.Write(ctrl, &pb.RelayWelcome{Error: why})
		_ = ctrl.Close()
		// 等客户端读到原因后自己断开，最多等一会儿。
		select {
		case <-conn.Context().Done():
		case <-time.After(2 * time.Second):
		}
		_ = conn.CloseWithError(0, why)
		return
	}
	defer s.unregister(c)
	// 告诉设备中继看到的它的地址：只告诉它自己，用来在已认证的成员连接里和对方交换、打洞（M2b）。
	if err := c.send(&pb.RelayWelcome{ObservedAddr: conn.RemoteAddr().String()}); err != nil {
		_ = conn.CloseWithError(0, "")
		return
	}
	_ = ctrl.SetDeadline(time.Time{})
	via := ""
	if c.viaTCP {
		via = i18n.T("（TCP）")
	}
	s.debugf(i18n.T("登记：%.8s…（%s）%s"), c.route, conn.RemoteAddr(), via)
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.writeEvents(c) }()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.readSubscriptions(c) }()
	for {
		str, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.handleOpen(ctx, c, str) }()
	}
}

// admit 校验令牌和设备签名，并登记设备；同一台设备重新登记时断开旧连接。返回拒绝原因（空表示成功）。
func (s *Server) admit(conn *quic.Conn, ctrl *quic.Stream, h *pb.RelayHello, viaTCP bool) (*client, string) {
	tokenID, ok := s.Tokens.Check(h.Token)
	if !ok {
		return nil, "invalid token"
	}
	st := conn.ConnectionState()
	exp, err := st.TLS.ExportKeyingMaterial(relay.ExporterLabel, nil, relay.ExporterLen)
	if err != nil {
		return nil, "internal error"
	}
	if len(h.DeviceKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(ed25519.PublicKey(h.DeviceKey), relay.RegisterMessage(exp), h.Signature) {
		return nil, "invalid signature"
	}
	c := &client{conn: conn, ctrl: ctrl, route: hex.EncodeToString(relay.RouteID(h.DeviceKey)), tokenID: tokenID, events: make(chan *pb.RelayEvent, eventQueue), viaTCP: viaTCP}
	s.mu.Lock()
	old := s.clients[c.route]
	if old == nil && len(s.clients) >= s.limits().MaxClients {
		s.mu.Unlock()
		return nil, "relay is full"
	}
	s.clients[c.route] = c
	for w := range s.subs[c.route] {
		s.enqueueLocked(w, c.route, true)
	}
	s.mu.Unlock()
	if old != nil {
		_ = old.conn.CloseWithError(codeClosed, "registered again")
	}
	return c, ""
}

// unregister 在连接结束时注销设备、取消它的订阅，并通知关注它的设备。
func (s *Server) unregister(c *client) {
	s.mu.Lock()
	wasOnline := s.clients[c.route] == c
	if wasOnline {
		delete(s.clients, c.route)
	}
	s.unsubscribeLocked(c)
	s.closeEventsLocked(c)
	if wasOnline {
		for w := range s.subs[c.route] {
			s.enqueueLocked(w, c.route, false)
		}
	}
	s.mu.Unlock()
}

func (s *Server) unsubscribeLocked(c *client) {
	for _, r := range c.subs {
		delete(s.subs[r], c)
		if len(s.subs[r]) == 0 {
			delete(s.subs, r)
		}
	}
	c.subs = nil
}

func (c *client) send(m proto.Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ctrl.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	defer c.ctrl.SetWriteDeadline(time.Time{})
	return wire.Write(c.ctrl, m)
}

// readSubscriptions 处理设备发来的订阅：整体替换它关注的路由 ID，并立即通知其中已经在线的。
func (s *Server) readSubscriptions(c *client) {
	for {
		var sub pb.RelaySubscribe
		if err := wire.ReadMax(c.ctrl, &sub, maxRoutes*40); err != nil {
			_ = c.conn.CloseWithError(0, "")
			return
		}
		if len(sub.Routes) > maxRoutes {
			_ = c.conn.CloseWithError(0, "too many subscriptions")
			return
		}
		s.mu.Lock()
		if c.closed {
			s.mu.Unlock()
			return
		}
		s.unsubscribeLocked(c)
		for _, r := range sub.Routes {
			key := hex.EncodeToString(r)
			if s.subs[key] == nil {
				s.subs[key] = map[*client]struct{}{}
			}
			s.subs[key][c] = struct{}{}
			c.subs = append(c.subs, key)
			if s.clients[key] != nil {
				s.enqueueLocked(c, key, true)
			}
		}
		s.mu.Unlock()
	}
}

// handleOpen 处理一次会话请求：找到目标设备，在它的连接上开一条流，然后原样转发两条流之间的字节。
func (s *Server) handleOpen(ctx context.Context, c *client, str *quic.Stream) {
	abort := func() {
		str.CancelRead(0)
		_ = str.Close()
	}
	_ = str.SetDeadline(time.Now().Add(handshakeTimeout))
	var o pb.RelayOpen
	if err := wire.ReadMax(str, &o, 256); err != nil {
		abort()
		return
	}
	reply := func(e string) bool { return wire.Write(str, &pb.RelayOpenReply{Error: e}) == nil }
	key := hex.EncodeToString(o.Route)
	s.mu.Lock()
	target := s.clients[key]
	full := s.perToken[c.tokenID] >= s.limits().MaxSessionsPerToken
	if target != nil && target != c && !full {
		s.perToken[c.tokenID]++
	}
	s.mu.Unlock()
	switch {
	case target == nil || target == c:
		reply(relay.OpenOffline)
		abort()
		return
	case full:
		reply("too many sessions")
		abort()
		return
	}
	defer func() {
		s.mu.Lock()
		s.perToken[c.tokenID]--
		if s.perToken[c.tokenID] <= 0 {
			delete(s.perToken, c.tokenID)
		}
		s.mu.Unlock()
	}()
	octx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	ts, err := target.conn.OpenStreamSync(octx)
	cancel()
	if err != nil {
		reply(relay.OpenOffline)
		abort()
		return
	}
	_ = ts.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := wire.Write(ts, &pb.RelayIncoming{}); err != nil || !reply("") {
		ts.CancelRead(0)
		_ = ts.Close()
		abort()
		return
	}
	_ = str.SetDeadline(time.Time{})
	_ = ts.SetDeadline(time.Time{})
	s.sessions.Add(1)
	s.total.Add(1)
	defer s.sessions.Add(-1)
	s.debugf(i18n.T("会话：%.8s… → %.8s…"), c.route, key)
	s.splice(str, ts)
}

// splice 双向转发两条流，直到任意一个方向结束；随后两条流都关闭。
func (s *Server) splice(a, b *quic.Stream) {
	done := make(chan struct{}, 2)
	rate := s.Limits.SessionRate
	cp := func(dst, src *quic.Stream) {
		n, _ := io.Copy(&rateWriter{w: dst, rate: rate, start: time.Now()}, src)
		s.bytes.Add(n)
		done <- struct{}{}
	}
	go cp(b, a)
	go cp(a, b)
	<-done
	for _, st := range []*quic.Stream{a, b} {
		st.CancelRead(0)
		st.CancelWrite(0)
	}
	<-done
}

// rateWriter 把写入速度限制在 rate 字节/秒以内（rate 为 0 时不限）。
type rateWriter struct {
	w     io.Writer
	rate  int64
	start time.Time
	n     int64
}

func (r *rateWriter) Write(p []byte) (int, error) {
	n, err := r.w.Write(p)
	r.n += int64(n)
	if r.rate > 0 {
		want := time.Duration(float64(r.n) / float64(r.rate) * float64(time.Second))
		if d := want - time.Since(r.start); d > 0 {
			time.Sleep(d)
		}
	}
	return n, err
}

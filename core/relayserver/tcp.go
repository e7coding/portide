package relayserver

import (
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"portide/core/relay"
)

// 中继的 TCP 备用通道（M6b）的服务端：UDP 被防火墙挡住时，客户端把同样的 QUIC 包放进 TLS-over-TCP 里发。
// 帧格式见 relay.ReadFrame（2 字节长度 + 包），长度必须是 1–65535；客户端一侧见 relay.DialTCP。

var (
	errBadFrame   = errors.New("relay: bad tcp frame")
	errUnknownTCP = errors.New("relay: no such tcp connection")
)

// tcpQueueLen 是所有连接共用的入站队列的长度：各连接读到、还没被 ReadFrom 取走的包合起来最多这么多；
// 队列满了，各连接的读协程都停在入队上，不再从各自的 TCP 连接读（TCP 反压）。
const tcpQueueLen = 256

// tcpMaxFrame 是服务端每条连接接受的最大包长。quic-go 读包的缓冲区只有 1452 字节，比它大的包交给
// ReadFrom 也只会让连接被断开，所以超过这个长度的帧直接断开连接（长度 0 同样断开）。
const tcpMaxFrame = 2048

type tcpPacket struct {
	data []byte
	addr net.Addr
	src  *tcpServerConn // 来自哪条连接：包比缓冲区大时要断开它
}

// tcpPacketConn 把监听的 TCP 端口上所有的 TLS 连接当作一个 net.PacketConn：
// 每条连接有自己的地址值（*tcpConnAddr，字符串形式是它的 RemoteAddr），ReadFrom 返回任意连接上的下一个包，
// WriteTo、Established、CloseAddr 按地址值找回那条连接——认的是连接本身，不是 ip:port，
// 所以新连接复用了旧连接的源 ip:port 时，给旧连接的调用不会落到新连接上。
//
// 需要强行断开的地方（包太大、帧不对、CloseAddr、握手超时、Close）一律直接关底层的 TCP 连接，
// 不调 tls.Conn.Close：后者要先写 close_notify，对端不读、发送缓冲区满时会卡到写超时（5 秒），
// 而 ReadFrom 是所有 TCP 连接共用的唯一读循环。
// 服务端把它交给 quic.Transport，同一个 Transport 就能同时收 UDP 之外的这批连接。
type tcpPacketConn struct {
	ln         net.Listener
	tlsConf    *tls.Config
	maxPending int

	// 可在测试里改小
	handshakeTimeout time.Duration // 外层 TLS 握手，以及 TLS 完成后等 QUIC 握手完成（Established）的时间
	writeTimeout     time.Duration

	in     chan tcpPacket
	closed chan struct{}
	once   sync.Once

	mu      sync.Mutex
	conns   map[*tcpServerConn]struct{} // 已完成 TLS 的连接
	pending int                         // 还没 Established 的连接数（含正在做 TLS 握手的）

	dmu     sync.Mutex
	rdl     time.Time
	rdlWake chan struct{} // 读截止时间被改动时关闭，唤醒阻塞中的 ReadFrom
}

type tcpServerConn struct {
	raw     net.Conn  // 底层 TCP 连接：强行断开时关它
	tc      *tls.Conn // TLS 完成后设置，之后才登记进 conns
	addr    *tcpConnAddr
	wmu     sync.Mutex
	pending bool // 受 tcpPacketConn.mu 保护
}

// tcpConnAddr 是一条 TCP 连接的地址：String 是对端的 ip:port，但每条连接各有一个，按指针区分。
type tcpConnAddr struct {
	sc     *tcpServerConn
	remote net.Addr
}

func (a *tcpConnAddr) Network() string { return a.remote.Network() }
func (a *tcpConnAddr) String() string  { return a.remote.String() }

func newTCPPacketConn(ln net.Listener, tlsConf *tls.Config, maxPending int) *tcpPacketConn {
	return newTCPPacketConnTimeout(ln, tlsConf, maxPending, handshakeTimeout)
}

// newTCPPacketConnTimeout 同 newTCPPacketConn，但握手超时由调用方给出（测试里改小）；必须在启动 acceptLoop 之前定下来。
func newTCPPacketConnTimeout(ln net.Listener, tlsConf *tls.Config, maxPending int, hs time.Duration) *tcpPacketConn {
	p := &tcpPacketConn{
		ln: ln, tlsConf: tlsConf, maxPending: maxPending,
		handshakeTimeout: hs, writeTimeout: handshakeTimeout,
		in: make(chan tcpPacket, tcpQueueLen), closed: make(chan struct{}),
		conns: make(map[*tcpServerConn]struct{}), rdlWake: make(chan struct{}),
	}
	go p.acceptLoop()
	return p
}

func (p *tcpPacketConn) acceptLoop() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			select {
			case <-p.closed:
				return
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond) // 瞬时错误（如文件句柄用尽）：稍等再试
			continue
		}
		p.mu.Lock()
		full := p.pending >= p.maxPending
		if !full {
			p.pending++
		}
		p.mu.Unlock()
		if full {
			c.Close()
			continue
		}
		go p.serve(c)
	}
}

// serve 处理一条连接：TLS 握手、登记、然后一直读帧。调用时这条连接已计入 pending。
func (p *tcpPacketConn) serve(raw net.Conn) {
	sc := &tcpServerConn{raw: raw, pending: true}
	sc.addr = &tcpConnAddr{sc: sc, remote: raw.RemoteAddr()}
	defer func() {
		raw.Close()
		p.mu.Lock()
		if sc.pending {
			sc.pending = false
			p.pending--
		}
		delete(p.conns, sc)
		p.mu.Unlock()
	}()

	tc := tls.Server(raw, p.tlsConf)
	_ = raw.SetDeadline(time.Now().Add(p.handshakeTimeout))
	if err := tc.Handshake(); err != nil {
		return
	}
	_ = raw.SetDeadline(time.Time{})
	sc.tc = tc

	p.mu.Lock()
	select {
	case <-p.closed:
		p.mu.Unlock()
		return
	default:
	}
	p.conns[sc] = struct{}{}
	p.mu.Unlock()

	// TLS 之后迟迟不完成 QUIC 握手的连接：到点关掉，不让它一直占着名额
	timer := time.AfterFunc(p.handshakeTimeout, func() {
		p.mu.Lock()
		stale := sc.pending
		p.mu.Unlock()
		if stale {
			raw.Close()
		}
	})
	defer timer.Stop()

	buf := make([]byte, tcpMaxFrame)
	for {
		n, err := relay.ReadFrame(tc, buf)
		if err != nil || n == 0 { // 长度 0、超过 tcpMaxFrame 或读错：断开这条连接
			return
		}
		pkt := tcpPacket{data: append([]byte(nil), buf[:n]...), addr: sc.addr, src: sc}
		select {
		case p.in <- pkt:
		case <-p.closed:
			return
		}
	}
}

// lookup 按 ReadFrom 返回的地址值找回仍登记着的连接；调用时持有 p.mu。
func (p *tcpPacketConn) lookup(addr net.Addr) *tcpServerConn {
	a, ok := addr.(*tcpConnAddr)
	if !ok {
		return nil
	}
	if _, ok := p.conns[a.sc]; !ok {
		return nil
	}
	return a.sc
}

// Established 在 QUIC 握手完成后由服务端调用：这条连接不再算"未完成"。
func (p *tcpPacketConn) Established(addr net.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sc := p.lookup(addr); sc != nil && sc.pending {
		sc.pending = false
		p.pending--
	}
}

// CloseAddr 关闭并忘掉 addr 对应的连接（未知则什么也不做）。QUIC 连接结束时由服务端调用，
// 免得已经 Established 的 TCP 连接在对端不关的情况下一直占着。
func (p *tcpPacketConn) CloseAddr(addr net.Addr) {
	p.mu.Lock()
	sc := p.lookup(addr)
	if sc != nil {
		delete(p.conns, sc)
		if sc.pending {
			sc.pending = false
			p.pending--
		}
	}
	p.mu.Unlock()
	if sc != nil {
		sc.raw.Close()
	}
}

// closeAll 断开所有已完成 TLS 的连接，监听照常。各连接的 serve 读出错后自己从 conns 里删掉。
func (p *tcpPacketConn) closeAll() {
	p.mu.Lock()
	conns := make([]net.Conn, 0, len(p.conns))
	for sc := range p.conns {
		conns = append(conns, sc.raw)
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// Count 返回当前已完成 TLS 的 TCP 连接数。
func (p *tcpPacketConn) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

func (p *tcpPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		p.dmu.Lock()
		dl, wake := p.rdl, p.rdlWake
		p.dmu.Unlock()
		var timeout <-chan time.Time
		var tm *time.Timer
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, nil, deadlineError{}
			}
			tm = time.NewTimer(d)
			timeout = tm.C
		}
		select {
		case pkt := <-p.in:
			if tm != nil {
				tm.Stop()
			}
			if len(pkt.data) > len(b) { // 包比缓冲区大：断开这条连接，丢掉这个包，继续读下一个
				pkt.src.raw.Close()
				continue
			}
			return copy(b, pkt.data), pkt.addr, nil
		case <-p.closed:
			if tm != nil {
				tm.Stop()
			}
			return 0, nil, net.ErrClosed
		case <-timeout:
			return 0, nil, deadlineError{}
		case <-wake:
			if tm != nil {
				tm.Stop()
			}
		}
	}
}

func (p *tcpPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if len(b) == 0 { // 长度 0 是"关闭连接"的信号，不能当作包发
		return 0, errBadFrame
	}
	p.mu.Lock()
	sc := p.lookup(addr)
	p.mu.Unlock()
	if sc == nil {
		return 0, errUnknownTCP
	}
	sc.wmu.Lock()
	defer sc.wmu.Unlock()
	_ = sc.tc.SetWriteDeadline(time.Now().Add(p.writeTimeout))
	if err := relay.WriteFrame(sc.tc, b); err != nil {
		sc.raw.Close() // 写了一半的帧会让流错位，这条连接不能再用
		return 0, err
	}
	return len(b), nil
}

// Close 关闭监听和所有连接。
func (p *tcpPacketConn) Close() error {
	var err error
	p.once.Do(func() {
		close(p.closed)
		err = p.ln.Close()
		p.mu.Lock()
		conns := make([]net.Conn, 0, len(p.conns))
		for sc := range p.conns {
			conns = append(conns, sc.raw)
		}
		p.mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return err
}

func (p *tcpPacketConn) LocalAddr() net.Addr { return p.ln.Addr() }

func (p *tcpPacketConn) SetReadDeadline(t time.Time) error {
	p.dmu.Lock()
	p.rdl = t
	close(p.rdlWake)
	p.rdlWake = make(chan struct{})
	p.dmu.Unlock()
	return nil
}

func (p *tcpPacketConn) SetDeadline(t time.Time) error { return p.SetReadDeadline(t) }

// 写有固定的超时，不支持单独设置。
func (p *tcpPacketConn) SetWriteDeadline(time.Time) error { return nil }

// SetReadBuffer、SetWriteBuffer 什么也不做：quic-go 找不到它们时会打一条讲 UDP 缓冲区的警告，对 TCP 通道是误导。
func (p *tcpPacketConn) SetReadBuffer(int) error  { return nil }
func (p *tcpPacketConn) SetWriteBuffer(int) error { return nil }

// deadlineError 满足 net.Error 的 Timeout，与 os.ErrDeadlineExceeded 的行为一致。
type deadlineError struct{}

func (deadlineError) Error() string   { return "relay: read deadline exceeded" }
func (deadlineError) Timeout() bool   { return true }
func (deadlineError) Temporary() bool { return true }

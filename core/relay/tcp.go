package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

// 中继的 TCP 备用通道（M6b）的客户端：UDP 被防火墙挡住时，把同样的 QUIC 包放进 TLS-over-TCP 里发。
// 帧格式见 ReadFrame（2 字节长度 + 包），长度必须是 1–65535。

var errBadFrame = errors.New("relay: bad tcp frame")

// handshakeTimeout 是 TCP 通道外层 TLS 握手的时限。
const handshakeTimeout = 10 * time.Second

// tcpClientConn 是客户端一侧：只和一条 TLS 连接通信的 PacketConn，WriteTo 忽略地址参数。
// 和服务端（relayserver）一样，关闭时直接关底层 TCP 连接，不写 close_notify（对端不读时会卡住）。
// 读包直接读进调用方（quic-go）的缓冲区，比它大的包断开连接，不另设缓冲区。
type tcpClientConn struct {
	conn net.Conn // TLS 连接
	raw  net.Conn // 底层 TCP 连接
	rmu  sync.Mutex
	wmu  sync.Mutex
}

// DialTCP 拨号中继的 TCP 端口 addr 并完成外层 TLS（按指纹校验、ALPN portide-relay-tcp/1），
// 返回的 PacketConn 可以交给 quic.Transport，在它上面再跑和 UDP 时一样的 QUIC。
func DialTCP(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), addr string, fp [32]byte) (net.PacketConn, error) {
	raw, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(raw, clientTLS(fp, ALPNTCP))
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		raw.Close()
		return nil, err
	}
	return &tcpClientConn{conn: tc, raw: raw}, nil
}

func (c *tcpClientConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	n, err := ReadFrame(c.conn, b)
	if err == nil && n == 0 {
		err = errBadFrame
	}
	if err != nil {
		if errors.Is(err, ErrPacketTooLarge) || errors.Is(err, errBadFrame) {
			c.raw.Close() // 流已经不可信，断开
		}
		return 0, nil, err
	}
	return n, c.conn.RemoteAddr(), nil
}

func (c *tcpClientConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	if len(b) == 0 {
		return 0, errBadFrame
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := WriteFrame(c.conn, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *tcpClientConn) Close() error                       { return c.raw.Close() }
func (c *tcpClientConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *tcpClientConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *tcpClientConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *tcpClientConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *tcpClientConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// 同服务端的 tcpPacketConn：让 quic-go 不打 UDP 缓冲区的警告。
func (c *tcpClientConn) SetReadBuffer(int) error  { return nil }
func (c *tcpClientConn) SetWriteBuffer(int) error { return nil }

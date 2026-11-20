package relay

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// Addr 是经中继的会话的地址：两端的 QUIC 看到的对方地址都是它。Relay 是这条会话所经中继的地址。
type Addr struct{ Relay string }

func (Addr) Network() string { return "portide-relay" }

// String 返回 "relay:中继地址"；Relay 为空时返回 "relay"（升级前数据库里记下的就是它）。
func (a Addr) String() string {
	if a.Relay == "" {
		return "relay"
	}
	return "relay:" + a.Relay
}

// RelayedVia 解析 Addr.String() 的结果：s 是经中继的地址时返回中继地址（旧格式 "relay" 时为空）和 true。
func RelayedVia(s string) (relayAddr string, ok bool) {
	if s == "relay" {
		return "", true
	}
	if a, ok := strings.CutPrefix(s, "relay:"); ok {
		return a, true
	}
	return "", false
}

// IsRelayed 报告 addr 是否是经中继的会话的地址。
func IsRelayed(addr net.Addr) bool {
	_, ok := addr.(Addr)
	return ok
}

// ErrPacketTooLarge 表示收到的包比调用方给的缓冲区大（QUIC 的包不会超过 64 KiB）。
var ErrPacketTooLarge = errors.New("relay: packet larger than buffer")

// ReadFrame 读一个"2 字节长度 + 包"的帧到 b，返回包长。包比 b 大时丢弃它并返回 ErrPacketTooLarge。
// 会话流和 TCP 通道都用这种帧，中继的服务端（relayserver）也用它。
func ReadFrame(r io.Reader, b []byte) (int, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n > len(b) {
		if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
			return 0, err
		}
		return 0, ErrPacketTooLarge
	}
	if _, err := io.ReadFull(r, b[:n]); err != nil {
		return 0, err
	}
	return n, nil
}

// WriteFrame 把 b 写成一个帧（一次 Write，调用方负责串行化）。
func WriteFrame(w io.Writer, b []byte) error {
	if len(b) > 0xFFFF {
		return ErrPacketTooLarge
	}
	frame := make([]byte, 2+len(b))
	binary.BigEndian.PutUint16(frame, uint16(len(b)))
	copy(frame[2:], b)
	_, err := w.Write(frame)
	return err
}

// packetConn 把一条会话流当作 UDP 用：每个 QUIC 包写成"2 字节长度 + 包"。
// 中继原样转发流上的字节，所以端到端的 QUIC 包对中继来说只是加密后的数据（设计方案 4.2 第 3 条）。
// 流是可靠、有序的，内层 QUIC 不会丢包，只是失去了乱序到达的好处。
type packetConn struct {
	str  *quic.Stream
	addr Addr // 两端都把它当作对方的地址
	rmu  sync.Mutex
	wmu  sync.Mutex
}

func newPacketConn(str *quic.Stream, relayAddr string) *packetConn {
	return &packetConn{str: str, addr: Addr{Relay: relayAddr}}
}

func (p *packetConn) ReadFrom(b []byte) (int, net.Addr, error) {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	n, err := ReadFrame(p.str, b)
	if err != nil {
		return 0, nil, err
	}
	return n, p.addr, nil
}

func (p *packetConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if err := WriteFrame(p.str, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close 结束会话：不再读，也不再写。
func (p *packetConn) Close() error {
	p.str.CancelRead(0)
	return p.str.Close()
}

func (p *packetConn) LocalAddr() net.Addr                { return p.addr }
func (p *packetConn) SetDeadline(t time.Time) error      { return p.str.SetDeadline(t) }
func (p *packetConn) SetReadDeadline(t time.Time) error  { return p.str.SetReadDeadline(t) }
func (p *packetConn) SetWriteDeadline(t time.Time) error { return p.str.SetWriteDeadline(t) }

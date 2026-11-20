// Package transport 负责 QUIC 监听与拨号（设计方案 4.4）。
//
// TLS 只提供加密通道：双方都不出示设备身份。服务端每次握手都现生成一张一次性证书，
// 客户端不出示证书，所以探测端口的外人拿不到任何长期密钥（设计方案 4.2"已知限制"的修复）。
// 设备身份在加密通道内由 core/memberauth（成员连接）或 core/pairing（配对连接）证明，
// 两者都绑定本条连接的 TLS 导出值。
//
// 同一个 UDP 端口承载两种入站连接：
//   - ALPN "portide/2"：成员连接；
//   - ALPN "portide-pair/2"：配对连接，只在邀请进行中时开放。
//
// 同一个端点还用来连中继（DialRelay，中继看到的就是本机监听端口的公网地址）；
// 经中继的成员连接跑在中继会话的虚拟 UDP 上（DialMemberOver / AcceptMemberOver），每个会话一个独立的 QUIC 端点。
//
// Accept 把两种连接放进同一个队列，调用方必须按 ALPN 分发；SetPairing(false) 只拒绝之后的配对握手，
// 不会关闭已经接受的配对连接。
package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	ALPNMember = "portide/2"
	ALPNPair   = "portide-pair/2"
)

// 设备之间的连接：每 5 秒发一次保活，15 秒收不到对方任何包就当作断开（对方突然断网、被系统冻结时，
// 另一端大约 15～20 秒后显示离线；正常退出时会发关闭连接，对方立即显示离线）。
// 空闲超时取两端的较小值，所以对方是旧版本时也按 15 秒算。
var quicConfig = &quic.Config{
	MaxIdleTimeout:  15 * time.Second,
	KeepAlivePeriod: 5 * time.Second,
	// 首次连接时系统可能弹出网络权限提示，给握手留足时间。
	HandshakeIdleTimeout: 30 * time.Second,
}

// Transport 是一个设备的 QUIC 端点。
type Transport struct {
	pairing atomic.Bool
	udp     *net.UDPConn
	tr      *quic.Transport
	ln      *quic.Listener
}

// Listen 在 addr（例如 ":7443"）上创建端点。
func Listen(addr string) (*Transport, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	t := &Transport{udp: udp, tr: &quic.Transport{Conn: udp}}
	base := serverTLS(func(hello *tls.ClientHelloInfo) string {
		if t.pairing.Load() && slices.Contains(hello.SupportedProtos, ALPNPair) {
			return ALPNPair
		}
		return ALPNMember
	})
	ln, err := t.tr.Listen(base, quicConfig)
	if err != nil {
		udp.Close()
		return nil, err
	}
	t.ln = ln
	return t, nil
}

// serverTLS 返回入站连接的 TLS 配置：按 alpn 的选择协商协议，每次握手现生成一张一次性证书
// （同一个守护进程的多次连接之间也无法据此关联）。
func serverTLS(alpn func(*tls.ClientHelloInfo) string) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		SessionTicketsDisabled: true,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			cert, err := ephemeralCertificate()
			if err != nil {
				return nil, err
			}
			return &tls.Config{
				Certificates:           []tls.Certificate{cert},
				MinVersion:             tls.VersionTLS13,
				NextProtos:             []string{alpn(hello)},
				SessionTicketsDisabled: true,
			}, nil
		},
	}
}

// Addr 返回实际监听地址。
func (t *Transport) Addr() *net.UDPAddr { return t.udp.LocalAddr().(*net.UDPAddr) }

// SetPairing 打开或关闭配对 ALPN。
func (t *Transport) SetPairing(on bool) { t.pairing.Store(on) }

// Accept 返回下一条已完成握手的入站连接（成员或配对，按 ALPN 区分）。
func (t *Transport) Accept(ctx context.Context) (*quic.Conn, error) { return t.ln.Accept(ctx) }

// DialMember 以成员 ALPN 连接 addr。对方是谁由随后的成员握手确认。
func (t *Transport) DialMember(ctx context.Context, addr string) (*quic.Conn, error) {
	return t.dial(ctx, addr, ALPNMember)
}

// DialPair 以配对 ALPN 连接邀请方。对方是谁由配对协议确认。
func (t *Transport) DialPair(ctx context.Context, addr string) (*quic.Conn, error) {
	return t.dial(ctx, addr, ALPNPair)
}

// relayQUICConfig 是到中继的长连接：保活间隔短于中继的空闲超时（60 秒），NAT 映射也不会过期。
var relayQUICConfig = &quic.Config{
	MaxIdleTimeout:       60 * time.Second,
	KeepAlivePeriod:      15 * time.Second,
	HandshakeIdleTimeout: 10 * time.Second,
}

// DialRelay 用本端点连接中继（TLS 配置由 core/relay 提供，按中继指纹校验）。
func (t *Transport) DialRelay(ctx context.Context, addr string, tlsConf *tls.Config) (*quic.Conn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	return t.tr.Dial(ctx, udpAddr, tlsConf, relayQUICConfig)
}

// DialMemberOver 在 pc（中继会话的虚拟 UDP）上以成员 ALPN 发起一条连接。返回的 closer 关闭连接、
// 这条连接专用的 QUIC 端点和 pc；连接断开后调用方必须调用它。
func DialMemberOver(ctx context.Context, pc net.PacketConn) (*quic.Conn, func(), error) {
	tr := &quic.Transport{Conn: pc}
	closer := func() {
		_ = tr.Close()
		_ = pc.Close()
	}
	conn, err := tr.Dial(ctx, pc.LocalAddr(), clientTLS(ALPNMember), quicConfig)
	if err != nil {
		closer()
		return nil, nil, err
	}
	return conn, closer, nil
}

// AcceptMemberOver 在 pc（中继会话的虚拟 UDP）上接受一条成员连接；closer 同 DialMemberOver。
// 经中继不做配对：配对只在局域网内进行。
func AcceptMemberOver(ctx context.Context, pc net.PacketConn) (*quic.Conn, func(), error) {
	tr := &quic.Transport{Conn: pc}
	closer := func() {
		_ = tr.Close()
		_ = pc.Close()
	}
	ln, err := tr.Listen(serverTLS(func(*tls.ClientHelloInfo) string { return ALPNMember }), quicConfig)
	if err != nil {
		closer()
		return nil, nil, err
	}
	conn, err := ln.Accept(ctx)
	if err != nil {
		closer()
		return nil, nil, err
	}
	return conn, closer, nil
}

// Close 关闭监听、所有连接和底层 UDP 套接字。
func (t *Transport) Close() error {
	err := t.tr.Close()
	if cerr := t.udp.Close(); err == nil && !errors.Is(cerr, net.ErrClosed) {
		err = cerr
	}
	return err
}

// ALPN 返回连接协商出的应用协议。
func ALPN(conn *quic.Conn) string { return conn.ConnectionState().TLS.NegotiatedProtocol }

func (t *Transport) dial(ctx context.Context, addr, alpn string) (*quic.Conn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	return t.tr.Dial(ctx, udpAddr, clientTLS(alpn), quicConfig)
}

// clientTLS 返回出站连接的 TLS 配置。服务端证书是一次性的，没有可校验的身份：只要求它是一张结构正确的
// Ed25519 证书，真正的身份校验在加密通道内完成。
func clientTLS(alpn string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: requireEd25519,
		MinVersion:            tls.VersionTLS13,
		NextProtos:            []string{alpn},
	}
}

func requireEd25519(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) != 1 {
		return fmt.Errorf("transport: expected 1 peer certificate, got %d", len(rawCerts))
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("transport: parse peer certificate: %w", err)
	}
	if _, ok := cert.PublicKey.(ed25519.PublicKey); !ok {
		return errors.New("transport: peer key is not Ed25519")
	}
	return nil
}

// ephemeralCertificate 生成一张一次性的自签证书，用完即弃。
func ephemeralCertificate() (tls.Certificate, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "portide"},
		// 固定的有效期：证书里不能带出本机时钟（时钟偏差也是一种可追踪的特征）。
		// 客户端不校验有效期（只要求是 Ed25519 证书），所以日期本身无关紧要。
		NotBefore:   time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:    time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

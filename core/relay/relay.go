// Package relay 是中继 portide-relay（设计方案 4.2、8）的协议和客户端：设备在线登记、上线通知、会话转发。
// 中继的服务端在 relayserver 包里，使用 AGPL-3.0；本包是客户端会导入的部分，使用 Apache-2.0。
//
// 中继不在设备圈里，也不参与端到端加密：设备之间经中继建立的仍是普通的成员连接（portide/2），
// 中继只把两端的加密 QUIC 包原样搬过去。中继看得到公网 IP、路由 ID、连接时间和字节数，
// 看不到文件、文本、设备名和成员名单（设计方案 4.3）。
package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// ALPN 是设备与中继之间的协议。
	ALPN = "portide-relay/1"
	// ALPNTCP 是外层 TCP+TLS 备用通道的协议：QUIC 包放在这条 TLS 流里，内层 QUIC 仍用 ALPN。
	ALPNTCP = "portide-relay-tcp/1"
	// ExporterLabel 是登记签名使用的 TLS 导出标签。
	ExporterLabel = "portide-relay-register-v1"
	// ExporterLen 是导出值的字节数。
	ExporterLen = 32
	// TokenSize 是访问令牌的字节数。
	TokenSize = 32
	// DefaultPort 是中继的默认 UDP 端口。
	DefaultPort = "7443"
	// OpenOffline 是中继回复"会话目标不在线"时 RelayOpenReply 的错误文本。
	OpenOffline = "offline"

	registerContext = "portide-relay-register-v1\x00"
	routeContext    = "portide-route-v1\x00"
	configScheme    = "portide-relay"
)

// RouteID 是设备在中继上的路由 ID：设备公钥的带域分隔的哈希。它与 KeyID 不同，
// 中继据此转发会话；只有知道设备公钥的设备圈成员才算得出来，外人无法枚举（设计方案 4.2）。
func RouteID(pub ed25519.PublicKey) []byte {
	sum := sha256.Sum256(append([]byte(routeContext), pub...))
	return sum[:]
}

// RegisterMessage 是设备登记时签名的内容：绑定本条连接的 TLS 导出值，无法重放到别的连接。
func RegisterMessage(exporter []byte) []byte {
	return append([]byte(registerContext), exporter...)
}

// Fingerprint 是中继的指纹：中继公钥的 SHA-256。客户端按指纹校验中继，不依赖 CA 和域名。
func Fingerprint(pub ed25519.PublicKey) [32]byte {
	return sha256.Sum256(pub)
}

// Config 是客户端连接中继所需的全部信息，可以写成一个"中继配置串"（设计方案 8）。
type Config struct {
	Addr        string // host:port（UDP）
	TCPPort     string // TCP 备用通道的端口；为空表示和 UDP 端口相同
	Fingerprint [32]byte
	Token       []byte
}

// TCPAddr 返回 TCP 备用通道的地址 host:port；TCPPort 为空时用 UDP 端口。
func (c Config) TCPAddr() string {
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c.Addr
	}
	if c.TCPPort != "" {
		port = c.TCPPort
	}
	return net.JoinHostPort(host, port)
}

// ConfigString 返回完整的配置串（含令牌）：只用来保存和交给用户复制，不要写进日志。
func (c Config) ConfigString() string {
	q := url.Values{}
	q.Set("fp", hex.EncodeToString(c.Fingerprint[:]))
	q.Set("token", base64.RawURLEncoding.EncodeToString(c.Token))
	// 只在 TCP 端口与 UDP 端口不同时输出，同一个配置每次输出完全相同（设备圈同步按串比较）
	if c.TCPPort != "" {
		if _, port, err := net.SplitHostPort(c.Addr); err != nil || port != c.TCPPort {
			q.Set("tcp", c.TCPPort)
		}
	}
	return (&url.URL{Scheme: configScheme, Host: c.Addr, RawQuery: q.Encode()}).String()
}

// String fmt/log 打印 Config 时只显示隐去令牌的形式。
func (c Config) String() string {
	return c.Redacted()
}

// Redacted 返回隐去令牌的配置串，用于显示。
func (c Config) Redacted() string {
	return fmt.Sprintf("%s://%s?fp=%s…", configScheme, c.Addr, hex.EncodeToString(c.Fingerprint[:8]))
}

// ParseConfig 解析配置串。地址没写端口时用默认端口 7443。
func ParseConfig(s string) (Config, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != configScheme || u.Host == "" {
		return Config{}, errors.New("relay: not a portide-relay:// config string")
	}
	if u.User != nil || u.Path != "" || u.RawPath != "" || u.Fragment != "" {
		return Config{}, errors.New("relay: not a portide-relay:// config string")
	}
	q := u.Query()
	if len(q["fp"]) != 1 || len(q["token"]) != 1 || len(q["tcp"]) > 1 {
		return Config{}, errors.New("relay: not a portide-relay:// config string")
	}

	// 解析地址和端口
	host := u.Host
	port := DefaultPort
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		port = p
		// 空端口用默认端口
		if port == "" {
			port = DefaultPort
		}
	} else {
		// 没写端口时去掉 IPv6 地址的方括号
		host = strings.Trim(host, "[]")
	}

	if host == "" {
		return Config{}, errors.New("relay: not a portide-relay:// config string")
	}

	// 校验和解析端口
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return Config{}, errors.New("relay: config string has an invalid port")
	}

	addr := net.JoinHostPort(host, port)

	// 解析指纹
	fp, err := hex.DecodeString(q.Get("fp"))
	if err != nil || len(fp) != 32 {
		return Config{}, errors.New("relay: config string has no valid fingerprint")
	}

	// 解析令牌
	token, err := base64.RawURLEncoding.DecodeString(q.Get("token"))
	if err != nil || len(token) != TokenSize {
		return Config{}, errors.New("relay: config string has no valid token")
	}

	c := Config{Addr: addr, Token: token}
	if tcp, ok := q["tcp"]; ok {
		n, err := strconv.Atoi(tcp[0])
		if err != nil || n < 1 || n > 65535 {
			return Config{}, errors.New("relay: config string has an invalid tcp port")
		}
		// 规范化：数字写法统一，与 UDP 端口相同时视为没写，保证解析再输出稳定
		if p := strconv.Itoa(n); p != port {
			c.TCPPort = p
		}
	}
	copy(c.Fingerprint[:], fp)
	return c, nil
}

// ServerCertificate 用中继密钥生成中继的 TLS 证书。证书内容固定（固定序列号和有效期），
// 客户端只核对其中的公钥与指纹是否一致。
func ServerCertificate(key ed25519.PrivateKey) (tls.Certificate, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "portide-relay"},
		NotBefore:    time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	pub := key.Public().(ed25519.PublicKey)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// ErrFingerprint 表示中继出示的证书与配置里的指纹对不上（地址填错，或者被冒充）。
var ErrFingerprint = errors.New("relay: relay fingerprint mismatch")

// checkFingerprint 要求对方只出示一张证书，且其中的 Ed25519 公钥的指纹等于 fp。
func checkFingerprint(certs []*x509.Certificate, fp [32]byte) error {
	if len(certs) != 1 {
		return ErrFingerprint
	}
	pub, ok := certs[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return ErrFingerprint
	}
	got := Fingerprint(pub)
	if !bytes.Equal(got[:], fp[:]) {
		return ErrFingerprint
	}
	return nil
}

// ClientTLS 返回连接中继用的 TLS 配置：只接受公钥指纹为 fp 的 Ed25519 证书。
func ClientTLS(fp [32]byte) *tls.Config {
	return clientTLS(fp, ALPN)
}

// clientTLS 是 ClientTLS 的通用形式：内层 QUIC 和外层 TCP 通道的指纹校验相同，只有 ALPN 不同。
func clientTLS(fp [32]byte, alpn string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // 不用 CA：下面按指纹校验
		// 用 VerifyConnection 而不是 VerifyPeerCertificate：后者在会话恢复时不会被调用。
		VerifyConnection: func(cs tls.ConnectionState) error { return checkFingerprint(cs.PeerCertificates, fp) },
		MinVersion:       tls.VersionTLS13,
		NextProtos:       []string{alpn},
	}
}

package quicpin

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// ALPN 是 Portide 协议标识，主版本号写在这里用于版本协商。
const ALPN = "portide/1"

// AllowFunc 判断某个 KeyID 是否为成员。
type AllowFunc func(keyID string) bool

// AllowOnly 只接受给定的 KeyID。
func AllowOnly(ids ...string) AllowFunc {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(id string) bool { return set[id] }
}

// AllowAny 接受任何对端（仅供 M0 跨设备手动测试使用），可选地把对端 KeyID 交给 log。
func AllowAny(log func(keyID string)) AllowFunc {
	return func(id string) bool {
		if log != nil {
			log(id)
		}
		return true
	}
}

func verifyPeer(allow AllowFunc) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) != 1 {
			return fmt.Errorf("quicpin: expected 1 peer certificate, got %d", len(rawCerts))
		}
		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("quicpin: parse peer certificate: %w", err)
		}
		pub, ok := cert.PublicKey.(ed25519.PublicKey)
		if !ok {
			return errors.New("quicpin: peer key is not Ed25519")
		}
		if id := KeyIDOf(pub); !allow(id) {
			return fmt.Errorf("quicpin: peer %s is not a member", id)
		}
		return nil
	}
}

// ServerTLS 要求客户端出示证书，并按白名单校验。
func ServerTLS(id *Identity, allow AllowFunc) *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{id.Cert},
		ClientAuth:            tls.RequireAnyClientCert,
		VerifyPeerCertificate: verifyPeer(allow),
		MinVersion:            tls.VersionTLS13,
		NextProtos:            []string{ALPN},
		// 恢复会话时不会调用 VerifyPeerCertificate，会绕过成员校验；服务端默认开启
		// session ticket，必须显式关闭以保证每次连接都走完整校验。
		SessionTicketsDisabled: true,
	}
}

// ClientTLS 出示本机证书，并按白名单校验服务端。
func ClientTLS(id *Identity, allow AllowFunc) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Cert},
		// 没有 CA：关闭默认证书链校验，全部交给 VerifyPeerCertificate 按公钥白名单判断。
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: verifyPeer(allow),
		MinVersion:            tls.VersionTLS13,
		NextProtos:            []string{ALPN},
	}
}

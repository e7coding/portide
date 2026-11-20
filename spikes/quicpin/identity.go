// Package quicpin 验证：Ed25519 自签证书 + QUIC 上的双向 TLS 1.3 + 按公钥白名单校验对端。
// M0 一次性验证代码，不进入正式实现。
package quicpin

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"strings"
	"time"
)

// Identity 是一台设备的长期身份：私钥 + 由它自签的证书。
type Identity struct {
	Key   ed25519.PrivateKey
	Cert  tls.Certificate
	KeyID string // 公钥 SHA-256 的大写十六进制，作为成员名单里的设备标识
}

// KeyIDOf 计算公钥对应的设备标识。按公钥而不是证书 DER 计算，证书重新签发后标识不变。
func KeyIDOf(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// NewIdentity 生成新的 Ed25519 密钥和十年有效的自签证书。
func NewIdentity() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "portide-device"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, err
	}
	return &Identity{
		Key:   priv,
		Cert:  tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv},
		KeyID: KeyIDOf(pub),
	}, nil
}

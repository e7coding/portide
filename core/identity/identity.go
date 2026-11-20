// Package identity 管理设备的长期身份：Ed25519 密钥、设备标识 KeyID 和由它派生的自签 TLS 证书。
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Identity 是一台设备的身份。私钥只存在于本机（见 keystore），从不发送给其他设备。
type Identity struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
	KeyID   string // 公钥 SHA-256 的大写十六进制
}

// KeyIDOf 计算公钥对应的设备标识。
func KeyIDOf(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// Generate 生成新的设备身份。
func Generate() (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return FromSeed(priv.Seed())
}

// FromSeed 由 32 字节种子恢复身份（keystore 中保存的就是种子）。
func FromSeed(seed []byte) (*Identity, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("identity: seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return &Identity{Private: priv, Public: pub, KeyID: KeyIDOf(pub)}, nil
}

// Seed 返回用于持久化的 32 字节种子。
func (id *Identity) Seed() []byte { return id.Private.Seed() }

// Certificate 生成一张由设备密钥自签的 TLS 证书。证书本身不需要持久化：对端只认公钥。
func (id *Identity) Certificate() (tls.Certificate, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "portide-device"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, id.Public, id.Private)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: id.Private}, nil
}

// PeerKey 从 TLS 对端证书链中取出 Ed25519 公钥。
func PeerKey(rawCerts [][]byte) (ed25519.PublicKey, error) {
	if len(rawCerts) != 1 {
		return nil, fmt.Errorf("identity: expected 1 peer certificate, got %d", len(rawCerts))
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return nil, fmt.Errorf("identity: parse peer certificate: %w", err)
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("identity: peer key is not Ed25519")
	}
	return pub, nil
}

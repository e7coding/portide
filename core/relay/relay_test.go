package relay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestConfigRoundTrip(t *testing.T) {
	c := Config{Addr: "relay.example.com:7443", Token: bytes.Repeat([]byte{7}, TokenSize)}
	c.Fingerprint[0], c.Fingerprint[31] = 0xAB, 0xCD
	s := c.ConfigString()
	if !strings.HasPrefix(s, "portide-relay://relay.example.com:7443?") {
		t.Fatalf("config string = %q", s)
	}
	got, err := ParseConfig("  " + s + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != c.Addr || got.Fingerprint != c.Fingerprint || !bytes.Equal(got.Token, c.Token) {
		t.Fatalf("round trip = %s", got.Redacted())
	}
	if strings.Contains(c.Redacted(), "token") {
		t.Fatal("redacted form must not contain the token")
	}
	tokenB64 := base64.RawURLEncoding.EncodeToString(c.Token)
	if strings.Contains(fmt.Sprint(c), tokenB64) {
		t.Fatal("fmt.Sprint must not leak the token")
	}
}

func TestParseConfigDefaultsAndErrors(t *testing.T) {
	c := Config{Addr: "1.2.3.4:7443", Token: make([]byte, TokenSize)}
	s := strings.Replace(c.ConfigString(), "1.2.3.4:7443", "1.2.3.4", 1)
	got, err := ParseConfig(s)
	if err != nil || got.Addr != "1.2.3.4:7443" {
		t.Fatalf("default port: %+v %v", got, err)
	}

	// IPv6 地址也应该用默认端口
	ipv6Config := Config{Addr: "[::1]:7443", Token: make([]byte, TokenSize)}
	ipv6S := strings.Replace(ipv6Config.ConfigString(), "[::1]:7443", "[::1]", 1)
	ipv6Got, err := ParseConfig(ipv6S)
	if err != nil || ipv6Got.Addr != "[::1]:7443" {
		t.Fatalf("IPv6 default port: %+v %v", ipv6Got, err)
	}

	// 空端口用默认端口
	emptyPortS := strings.Replace(c.ConfigString(), "1.2.3.4:7443", "1.2.3.4:", 1)
	emptyPortGot, err := ParseConfig(emptyPortS)
	if err != nil || emptyPortGot.Addr != "1.2.3.4:7443" {
		t.Fatalf("empty port: %+v %v", emptyPortGot, err)
	}

	for _, bad := range []string{
		"",
		"https://relay.example.com?fp=00&token=00",
		strings.Replace(c.ConfigString(), "fp=", "fp=zz", 1),
		strings.Replace(c.ConfigString(), "token=", "token=AAAA", 1),
		"portide-relay://?fp=" + strings.Repeat("0", 64),
		"portide-relay://host:0?fp=" + strings.Repeat("0", 64) + "&token=" + base64.RawURLEncoding.EncodeToString(make([]byte, TokenSize)),
		"portide-relay://host:99999?fp=" + strings.Repeat("0", 64) + "&token=" + base64.RawURLEncoding.EncodeToString(make([]byte, TokenSize)),
		"portide-relay://a@host?fp=" + strings.Repeat("0", 64) + "&token=" + base64.RawURLEncoding.EncodeToString(make([]byte, TokenSize)),
		"portide-relay://host/x?fp=" + strings.Repeat("0", 64) + "&token=" + base64.RawURLEncoding.EncodeToString(make([]byte, TokenSize)),
		"portide-relay://host?fp=" + strings.Repeat("0", 64) + "&token=" + base64.RawURLEncoding.EncodeToString(make([]byte, TokenSize)) + "&fp=" + strings.Repeat("0", 64),
	} {
		if _, err := ParseConfig(bad); err == nil {
			t.Errorf("ParseConfig(%q) must fail", bad)
		}
	}
}

func TestRouteIDIsNotKeyID(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	r := RouteID(pub)
	if len(r) != 32 {
		t.Fatalf("route id length = %d", len(r))
	}
	fp := Fingerprint(pub)
	if bytes.Equal(r, fp[:]) {
		t.Fatal("route id must be domain-separated from the plain key hash")
	}
}

func TestClientTLSPinsFingerprint(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	cert, err := ServerCertificate(priv)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	// 指纹匹配时接受
	good := ClientTLS(Fingerprint(pub))
	if err := good.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{parsed}}); err != nil {
		t.Fatalf("matching fingerprint rejected: %v", err)
	}

	// 指纹不对时拒绝
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if err := ClientTLS(Fingerprint(otherPub)).VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{parsed}}); err != ErrFingerprint {
		t.Fatalf("wrong fingerprint: err = %v", err)
	}

	// 零个证书时拒绝
	if err := good.VerifyConnection(tls.ConnectionState{PeerCertificates: nil}); err != ErrFingerprint {
		t.Fatalf("zero certificates: err = %v", err)
	}

	// 非 Ed25519 密钥时拒绝
	ecPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	ecDer, _ := x509.CreateCertificate(rand.Reader, ecTmpl, ecTmpl, &ecPriv.PublicKey, ecPriv)
	ecCert, _ := x509.ParseCertificate(ecDer)
	if err := good.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{ecCert}}); err != ErrFingerprint {
		t.Fatalf("non-Ed25519 key: err = %v", err)
	}

	// 真实握手：指纹对得上时成功
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("tls.Listen failed: %v", err)
	}
	defer listener.Close()

	acceptDone := make(chan struct{})
	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			// 读取确保握手完成
			var buf [1]byte
			conn.Read(buf[:])
			conn.Close()
		}
		close(acceptDone)
	}()

	goodConn, err := tls.Dial("tcp", listener.Addr().String(), ClientTLS(Fingerprint(pub)))
	if err != nil {
		t.Fatalf("tls.Dial with matching fingerprint failed: %v", err)
	}
	// 写入让服务端读取
	goodConn.Write([]byte{0})
	goodConn.Close()
	<-acceptDone

	// 真实握手：指纹对不上时失败
	acceptDone2 := make(chan struct{})
	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			var buf [1]byte
			conn.Read(buf[:])
			conn.Close()
		}
		close(acceptDone2)
	}()

	badConn, err := tls.Dial("tcp", listener.Addr().String(), ClientTLS(Fingerprint(otherPub)))
	if err == nil {
		<-acceptDone2
		t.Fatal("tls.Dial with wrong fingerprint should have failed")
	}
	if badConn != nil {
		badConn.Close()
	}
	<-acceptDone2
	if !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("error should mention fingerprint: %v", err)
	}
}

// 经中继的会话地址：旧数据库里的 "relay" 和带中继地址的 "relay:地址" 都算经中继，普通 IP:端口不算。
func TestRelayedViaParses(t *testing.T) {
	for _, c := range []struct {
		in   string
		addr string
		ok   bool
	}{
		{"relay", "", true},
		{"relay:1.2.3.4:7443", "1.2.3.4:7443", true},
		{Addr{Relay: "1.2.3.4:7443"}.String(), "1.2.3.4:7443", true},
		{"192.168.1.5:7443", "", false},
	} {
		addr, ok := RelayedVia(c.in)
		if addr != c.addr || ok != c.ok {
			t.Errorf("RelayedVia(%q) = %q, %v; want %q, %v", c.in, addr, ok, c.addr, c.ok)
		}
	}
	if (Addr{}).String() != "relay" {
		t.Errorf("empty Addr = %q, want relay", Addr{}.String())
	}
}

func TestParseConfigTCP(t *testing.T) {
	base := Config{Addr: "relay.example.com:7443", Token: bytes.Repeat([]byte{7}, TokenSize)}
	s := base.ConfigString()
	got, err := ParseConfig(s + "&tcp=443")
	if err != nil || got.TCPPort != "443" || got.TCPAddr() != "relay.example.com:443" {
		t.Fatalf("tcp=443: %+v %v", got, err)
	}
	got, err = ParseConfig(s)
	if err != nil || got.TCPPort != "" || got.TCPAddr() != got.Addr {
		t.Fatalf("no tcp: %+v %v", got, err)
	}
	for _, bad := range []string{"abc", "0", "70000", ""} {
		if _, err := ParseConfig(s + "&tcp=" + bad); err == nil {
			t.Fatalf("tcp=%q should fail", bad)
		}
	}
}

func TestConfigStringTCPStable(t *testing.T) {
	c := Config{Addr: "relay.example.com:7443", Token: bytes.Repeat([]byte{7}, TokenSize)}
	c.TCPPort = "7443"
	if s := c.ConfigString(); strings.Contains(s, "tcp=") {
		t.Fatalf("same port must not print tcp=: %s", s)
	}
	c.TCPPort = "443"
	s := c.ConfigString()
	if !strings.Contains(s, "tcp=443") || s != c.ConfigString() {
		t.Fatalf("config string = %s", s)
	}
	got, err := ParseConfig(s)
	if err != nil || got.ConfigString() != s {
		t.Fatalf("reparse = %s %v", got.ConfigString(), err)
	}
	// 写成与 UDP 端口相同、或带前导零，解析后输出一致
	c.TCPPort = ""
	p, err := ParseConfig(c.ConfigString() + "&tcp=07443")
	if err != nil || strings.Contains(p.ConfigString(), "tcp=") {
		t.Fatalf("normalize = %s %v", p.ConfigString(), err)
	}
}

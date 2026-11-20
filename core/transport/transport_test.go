package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// noApplicationProtocol 是 TLS 警报 no_application_protocol(120) 在 QUIC 中的错误码。
const noApplicationProtocol = quic.TransportErrorCode(0x100 + 120)

func listen(t *testing.T) *Transport {
	t.Helper()
	tr, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// acceptOne 在后台接受一条连接，返回接收通道。
func acceptOne(t *testing.T, tr *Transport) <-chan *quic.Conn {
	ch := make(chan *quic.Conn, 1)
	go func() {
		c, err := tr.Accept(ctx(t))
		if err == nil {
			ch <- c
		}
		close(ch)
	}()
	return ch
}

func serverKey(t *testing.T, conn *quic.Conn) ed25519.PublicKey {
	t.Helper()
	certs := conn.ConnectionState().TLS.PeerCertificates
	if len(certs) != 1 {
		t.Fatalf("expected 1 server certificate, got %d", len(certs))
	}
	return certs[0].PublicKey.(ed25519.PublicKey)
}

func TestMemberConnectionAndSharedExporter(t *testing.T) {
	server, client := listen(t), listen(t)
	accepted := acceptOne(t, server)
	conn, err := client.DialMember(ctx(t), server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "")
	in := <-accepted
	if in == nil || ALPN(in) != ALPNMember || ALPN(conn) != ALPNMember {
		t.Fatal("member connection not established with the member ALPN")
	}
	// 客户端不出示证书：服务端看不到客户端的任何密钥。
	if n := len(in.ConnectionState().TLS.PeerCertificates); n != 0 {
		t.Fatalf("server saw %d client certificates, want 0", n)
	}
	a := conn.ConnectionState().TLS
	b := in.ConnectionState().TLS
	x, err := a.ExportKeyingMaterial("test", nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	y, err := b.ExportKeyingMaterial("test", nil, 32)
	if err != nil || !bytes.Equal(x, y) {
		t.Fatalf("exporter differs between the two ends (err %v)", err)
	}
}

// 探测端口的外人每次拿到的都是不同的一次性证书，无法据此把多次连接关联到同一台设备。
func TestServerCertificateIsEphemeral(t *testing.T) {
	server, prober := listen(t), listen(t)
	var keys []ed25519.PublicKey
	for i := 0; i < 2; i++ {
		accepted := acceptOne(t, server)
		conn, err := prober.DialMember(ctx(t), server.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, serverKey(t, conn))
		conn.CloseWithError(0, "")
		if c := <-accepted; c != nil {
			c.CloseWithError(0, "")
		}
	}
	if keys[0].Equal(keys[1]) {
		t.Fatal("the server presented the same certificate key twice")
	}
}

func TestPairingALPNOnlyWhilePairing(t *testing.T) {
	inviter, joiner := listen(t), listen(t)
	_, err := joiner.DialPair(ctx(t), inviter.Addr().String())
	var te *quic.TransportError
	if !errors.As(err, &te) || te.ErrorCode != noApplicationProtocol {
		t.Fatalf("pairing while closed: err = %v, want no_application_protocol", err)
	}

	inviter.SetPairing(true)
	accepted := acceptOne(t, inviter)
	conn, err := joiner.DialPair(ctx(t), inviter.Addr().String())
	if err != nil {
		t.Fatalf("pairing dial: %v", err)
	}
	defer conn.CloseWithError(0, "")
	if in := <-accepted; in == nil || ALPN(in) != ALPNPair {
		t.Fatal("inviter should accept a pairing connection while pairing is open")
	}

	// 配对开放期间，成员 ALPN 照常可用。
	accepted = acceptOne(t, inviter)
	mc, err := joiner.DialMember(ctx(t), inviter.Addr().String())
	if err != nil {
		t.Fatalf("member dial while pairing: %v", err)
	}
	defer mc.CloseWithError(0, "")
	if in := <-accepted; in == nil || ALPN(in) != ALPNMember {
		t.Fatal("member ALPN must still work while pairing is open")
	}
}

func TestCertificateHasFixedValidity(t *testing.T) {
	server, client := listen(t), listen(t)
	accepted := acceptOne(t, server)
	conn, err := client.DialMember(ctx(t), server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "")
	if c := <-accepted; c != nil {
		defer c.CloseWithError(0, "")
	}
	certs := conn.ConnectionState().TLS.PeerCertificates
	if len(certs) != 1 {
		t.Fatalf("expected 1 server certificate, got %d", len(certs))
	}
	want := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if !certs[0].NotBefore.Equal(want) {
		t.Fatalf("NotBefore = %v, want %v", certs[0].NotBefore, want)
	}
	want = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	if !certs[0].NotAfter.Equal(want) {
		t.Fatalf("NotAfter = %v, want %v", certs[0].NotAfter, want)
	}
}

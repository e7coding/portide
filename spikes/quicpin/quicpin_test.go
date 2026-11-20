package quicpin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// bad_certificate（TLS alert 42）映射为 QUIC CRYPTO_ERROR 0x100+42 = 0x12a。
const wantBadCertCode = quic.TransportErrorCode(0x100 + 42)

func mustIdentity(t *testing.T) *Identity {
	t.Helper()
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func startServer(t *testing.T, id *Identity, allow AllowFunc) string {
	t.Helper()
	ln, err := Listen("127.0.0.1:0", id, allow)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = ln.Close() })
	go func() { _ = ServeEcho(ctx, ln) }()
	return ln.Addr().String()
}

func TestMembersEcho64MiB(t *testing.T) {
	srv, cli := mustIdentity(t), mustIdentity(t)
	addr := startServer(t, srv, AllowOnly(cli.KeyID))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := Dial(ctx, addr, cli, AllowOnly(srv.KeyID))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	res, err := EchoProbe(ctx, conn, 64)
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	t.Log(res)
}

func TestClientRejectsNonMemberServer(t *testing.T) {
	srv, cli, other := mustIdentity(t), mustIdentity(t), mustIdentity(t)
	addr := startServer(t, srv, AllowOnly(cli.KeyID))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, addr, cli, AllowOnly(other.KeyID))
	if err == nil {
		_ = conn.CloseWithError(0, "")
		t.Fatal("expected handshake to fail for a non-member server")
	}
	var transportErr *quic.TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("expected a *quic.TransportError, got %T: %v", err, err)
	}
	if transportErr.ErrorCode != wantBadCertCode {
		t.Fatalf("ErrorCode = %v, want %v (bad_certificate)", transportErr.ErrorCode, wantBadCertCode)
	}
	t.Logf("rejected as expected: %v", err)
}

func TestServerRejectsNonMemberClient(t *testing.T) {
	srv, cli, other := mustIdentity(t), mustIdentity(t), mustIdentity(t)
	addr := startServer(t, srv, AllowOnly(other.KeyID))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, addr, cli, AllowOnly(srv.KeyID))
	if err == nil {
		// TLS 1.3 下客户端可能先于服务端完成握手，拒绝会在首次读写时体现。
		_, err = Echo(ctx, conn, []byte("hello"))
		_ = conn.CloseWithError(0, "")
	}
	if err == nil {
		t.Fatal("expected a non-member client to be rejected")
	}
	var transportErr *quic.TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("expected a *quic.TransportError, got %T: %v", err, err)
	}
	if transportErr.ErrorCode != wantBadCertCode {
		t.Fatalf("ErrorCode = %v, want %v (bad_certificate)", transportErr.ErrorCode, wantBadCertCode)
	}
	t.Logf("rejected as expected: %v", err)
}

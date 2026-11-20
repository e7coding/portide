package relayserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"testing"
	"time"

	"portide/core/relay"
	"portide/core/transport"
)

// 端到端：两台设备各用自己的 QUIC 端点连中继，经中继建立一条成员 ALPN 的连接并传输数据。
func TestMemberConnectionOverRelay(t *testing.T) {
	r := startRelay(t, Limits{})
	ta, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ta.Close()
	tb, err := transport.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	laptopID, desktopID := mustID(t), mustID(t)
	laptop, err := relay.Connect(ctx, relay.Dialer{UDP: ta.DialRelay}, r.cfg, laptopID)
	if err != nil {
		t.Fatal(err)
	}
	defer laptop.Close()
	desktop, err := relay.Connect(ctx, relay.Dialer{UDP: tb.DialRelay}, r.cfg, desktopID)
	if err != nil {
		t.Fatal(err)
	}
	defer desktop.Close()

	payload := make([]byte, 4<<20)
	_, _ = rand.Read(payload)
	got := make(chan []byte, 1)
	go func() {
		pc, err := desktop.Accept(ctx)
		if err != nil {
			t.Error(err)
			got <- nil
			return
		}
		conn, closer, err := transport.AcceptMemberOver(ctx, pc)
		if err != nil {
			t.Error(err)
			got <- nil
			return
		}
		defer closer()
		if transport.ALPN(conn) != transport.ALPNMember || !relay.IsRelayed(conn.RemoteAddr()) {
			t.Errorf("alpn=%q remote=%v", transport.ALPN(conn), conn.RemoteAddr())
		}
		str, err := conn.AcceptStream(ctx)
		if err != nil {
			t.Error(err)
			got <- nil
			return
		}
		b, _ := io.ReadAll(str)
		_, _ = str.Write([]byte("收到"))
		_ = str.Close()
		got <- b
		<-conn.Context().Done()
	}()

	pc, err := laptop.Open(ctx, relay.RouteID(desktopID.Public))
	if err != nil {
		t.Fatal(err)
	}
	conn, closer, err := transport.DialMemberOver(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _ = str.Write(payload)
	_ = str.Close()
	reply, _ := io.ReadAll(str)
	if b := <-got; !bytes.Equal(b, payload) || string(reply) != "收到" {
		t.Fatalf("payload intact=%v reply=%q", bytes.Equal(b, payload), reply)
	}
	t.Logf("4 MiB through the relay in %v", time.Since(start))
	_ = conn.CloseWithError(0, "")
}

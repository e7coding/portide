package quicpin

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/quic-go/quic-go"
)

// HandshakeIdleTimeout 放宽到 30s（默认 5s）：iOS 首次运行会卡在系统的本地网络权限
// 弹窗上等待用户授权，默认超时会导致握手在用户点"允许"之前就失败。
var quicConf = &quic.Config{MaxIdleTimeout: 30 * time.Second, KeepAlivePeriod: 10 * time.Second, HandshakeIdleTimeout: 30 * time.Second}

// Listen 在 addr 上监听 QUIC，只接受白名单内的客户端。
func Listen(addr string, id *Identity, allow AllowFunc) (*quic.Listener, error) {
	return quic.ListenAddr(addr, ServerTLS(id, allow), quicConf)
}

// Dial 连接 addr，只接受白名单内的服务端。
func Dial(ctx context.Context, addr string, id *Identity, allow AllowFunc) (*quic.Conn, error) {
	return quic.DialAddr(ctx, addr, ClientTLS(id, allow), quicConf)
}

// ServeEcho 把每条流收到的数据原样写回，直到 ctx 结束或监听关闭。
func ServeEcho(ctx context.Context, ln *quic.Listener) error {
	for {
		conn, err := ln.Accept(ctx)
		if err != nil {
			return err
		}
		go func() {
			for {
				str, err := conn.AcceptStream(ctx)
				if err != nil {
					return
				}
				go func() {
					defer str.Close()
					_, _ = io.Copy(str, str)
				}()
			}
		}()
	}
}

// Echo 在新流上发送 payload，并读回服务端回显的全部数据。
func Echo(ctx context.Context, conn *quic.Conn, payload []byte) ([]byte, error) {
	str, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = str.SetDeadline(dl)
	}
	werr := make(chan error, 1)
	go func() {
		_, err := str.Write(payload)
		if cerr := str.Close(); err == nil {
			err = cerr
		}
		werr <- err
	}()
	got, rerr := io.ReadAll(str)
	if err := <-werr; err != nil {
		return nil, err
	}
	return got, rerr
}

// EchoProbe 回显 mib MiB 随机数据，校验一致性并返回耗时与吞吐描述。
func EchoProbe(ctx context.Context, conn *quic.Conn, mib int) (string, error) {
	payload := make([]byte, mib<<20)
	_, _ = rand.Read(payload)
	start := time.Now()
	got, err := Echo(ctx, conn, payload)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(got, payload) {
		return "", errors.New("quicpin: echo payload mismatch")
	}
	el := time.Since(start)
	return fmt.Sprintf("echo %d MiB in %s (%.1f MiB/s each way)", mib, el.Round(time.Millisecond), float64(mib)/el.Seconds()), nil
}

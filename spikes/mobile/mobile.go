// Package mobile 是 M0 的 gomobile 绑定入口（Swift 中的函数前缀为 Mobile）。仅用于验证。
package mobile

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"portide/spikes/hashprobe"
	"portide/spikes/pake"
	"portide/spikes/quicpin"
	"portide/spikes/sqliteprobe"
)

var (
	idOnce sync.Once
	id     *quicpin.Identity
	idErr  error

	srvMu      sync.Mutex
	srvLn      *quic.Listener
	lastSrvErr error
)

func identity() (*quicpin.Identity, error) {
	idOnce.Do(func() { id, idErr = quicpin.NewIdentity() })
	return id, idErr
}

// KeyID 返回本进程身份的 KeyID（首次调用时生成）。
func KeyID() (string, error) {
	me, err := identity()
	if err != nil {
		return "", err
	}
	return me.KeyID, nil
}

// StartQuicServer 在 port 上启动回显服务（接受任何对端），返回实际监听地址。重复调用返回已有地址。
func StartQuicServer(port int) (string, error) {
	srvMu.Lock()
	defer srvMu.Unlock()
	if srvLn != nil {
		return srvLn.Addr().String(), nil
	}
	me, err := identity()
	if err != nil {
		return "", err
	}
	ln, err := quicpin.Listen(fmt.Sprintf(":%d", port), me, quicpin.AllowAny(nil))
	if err != nil {
		return "", err
	}
	srvLn = ln
	go func() {
		err := quicpin.ServeEcho(context.Background(), ln)
		// 监听器可能意外失败（例如 iOS 挂起时系统回收了套接字），此时需要清除
		// srvLn 并记录错误，避免下次 StartQuicServer 调用误报已失效的旧地址。
		srvMu.Lock()
		if srvLn == ln {
			srvLn = nil
			lastSrvErr = err
		}
		srvMu.Unlock()
		_ = ln.Close()
	}()
	addr := ln.Addr().String()
	if lastSrvErr != nil {
		prevErr := lastSrvErr
		lastSrvErr = nil
		return fmt.Sprintf("previous server stopped: %v; %s", prevErr, addr), nil
	}
	return addr, nil
}

// QuicEcho 连接 addr（host:port），回显 mib MiB 数据，返回吞吐和对端 KeyID 前缀。
func QuicEcho(addr string, mib int) (string, error) {
	me, err := identity()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	peer := make(chan string, 1)
	conn, err := quicpin.Dial(ctx, addr, me, quicpin.AllowAny(func(k string) {
		select {
		case peer <- k:
		default:
		}
	}))
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	res, err := quicpin.EchoProbe(ctx, conn, mib)
	if err != nil {
		return "", err
	}
	var peerKey string
	select {
	case peerKey = <-peer:
	default:
		peerKey = "unknown"
	}
	return fmt.Sprintf("%s peer=%.16s…", res, peerKey), nil
}

// SQLiteProbe 见 sqliteprobe.Probe。
func SQLiteProbe(dir string) (string, error) { return sqliteprobe.Probe(dir) }

// HashThroughput 见 hashprobe.Throughput。
func HashThroughput(mib int) string { return hashprobe.Throughput(mib) }

// PakeSelfTest 见 pake.SelfTest。
func PakeSelfTest() (string, error) { return pake.SelfTest() }

// MemReport 返回 Go 运行时的内存占用。
func MemReport() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return fmt.Sprintf("go heapInuse=%.1fMiB sys=%.1fMiB numGC=%d", float64(m.HeapInuse)/(1<<20), float64(m.Sys)/(1<<20), m.NumGC)
}

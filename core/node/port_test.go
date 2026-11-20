package node

import (
	"context"
	"errors"
	"net"
	"testing"

	"portide/core/store"
)

func TestListenPortSetting(t *testing.T) {
	d := newDevice(t, "笔记本")
	if p := d.n.ListenPort(); p != DefaultListenPort {
		t.Fatalf("default port = %d", p)
	}
	for _, bad := range []int{-1, 65536} {
		if err := d.n.SetListenPort(bad); err == nil {
			t.Errorf("SetListenPort(%d) accepted", bad)
		}
	}
	if err := d.n.SetListenPort(0); err != nil || d.n.ListenPort() != 0 {
		t.Fatalf("SetListenPort(0): %v, port = %d", err, d.n.ListenPort())
	}
	d.n.opts.ListenAddr = ""
	if got := d.n.listenAddr(); got != ":0" {
		t.Fatalf("listenAddr = %q, want :0 from the setting", got)
	}
}

// 端口被占用时守护进程返回可判别的 ErrPortInUse。
func TestRunReportsPortInUse(t *testing.T) {
	busy, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	d := newDevice(t, "笔记本")
	d.n.opts.ListenAddr = busy.LocalAddr().String()
	if err := d.n.Run(context.Background()); !errors.Is(err, ErrPortInUse) {
		t.Fatalf("Run on a busy port: err = %v, want ErrPortInUse", err)
	}
}

// 端口设为 0 时由系统选一个，守护进程记下实际地址（局域网发现发布的也是它）。
func TestAutomaticListenPort(t *testing.T) {
	d := newDevice(t, "笔记本")
	_ = d.n.SetListenPort(0)
	d.n.opts.ListenAddr = ""
	d.start(t)
	addr, _, _ := d.n.Store().GetMeta(store.MetaDaemonAddr)
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil || ua.Port == 0 || ua.Port == DefaultListenPort {
		t.Fatalf("daemon addr = %q (%v), want a system-chosen port", addr, err)
	}
}

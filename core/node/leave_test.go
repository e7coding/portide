package node

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"portide/core/pairing"
	"portide/core/store"
)

// startStoppable 同 start，但返回一个停止守护进程的函数（可以重复调用）。
func (d *testDevice) startStoppable(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.n.Run(ctx); close(done) }()
	stop = func() { cancel(); <-done }
	t.Cleanup(stop)
	<-d.n.Ready()
	return stop
}

// reopen 关掉运行过守护进程的 Node，在同一个数据目录上重新打开一个（Node 只能 Run 一次）。
func (d *testDevice) reopen(t *testing.T) {
	t.Helper()
	_ = d.n.Close()
	n, err := Open(Options{
		DataDir: d.dir, Keys: d.keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true,
		PollInterval: 50 * time.Millisecond, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	d.n = n
}

// 只有本机的设备圈：直接 LeaveCircle，再用配对码加入别的设备圈。
func TestLeaveSoloCircleThenJoinAnother(t *testing.T) {
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	if err := desktop.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	ownCircle := desktop.n.CircleID()
	// 离开之前开放过的配对码要作废，不然加入笔记本的设备圈之后还能用它拉别的设备进来。
	sid, err := desktop.n.Store().CreatePairSession("12345678", pairing.CodeTTL)
	if err != nil {
		t.Fatal(err)
	}

	if err := desktop.n.LeaveCircle(); err != nil {
		t.Fatalf("LeaveCircle: %v", err)
	}
	if desktop.n.InCircle() {
		t.Fatal("still in a circle after leaving")
	}
	if ps, _ := desktop.n.Store().GetPairSession(sid); ps.Status != store.PairFailed {
		t.Fatalf("pending pair session status = %q, want failed", ps.Status)
	}
	if ps, _ := desktop.n.Store().PendingPairSession(time.Now()); ps != nil {
		t.Fatalf("pair session %d still pending", ps.ID)
	}
	// 重新打开也不在设备圈里（链确实从数据库里删了）。
	desktop.reopen(t)
	if desktop.n.InCircle() {
		t.Fatal("reopened node is still in a circle")
	}

	laptop.start(t)
	joinCircle(t, laptop, desktop)
	if got := desktop.n.CircleID(); got != laptop.n.CircleID() || got == ownCircle {
		t.Fatalf("circle = %s, want laptop's %s", got, laptop.n.CircleID())
	}
	desktop.start(t)
	eventually(t, "desktop online", func() bool { return online(t, laptop, desktop.n.id.KeyID) })
}

// 台式机在笔记本的设备圈里，要改为加入手机（自己新建了设备圈）的：请笔记本把它移出去（笔记本换新的发现密钥、
// 断开台式机），台式机删掉本机记录，再加入手机的设备圈。
func TestRequestLeaveThenJoinAnotherCircle(t *testing.T) {
	laptop, desktop, phone := newDevice(t, "笔记本"), newDevice(t, "台式机"), newDevice(t, "手机")
	laptop.start(t)
	joinCircle(t, laptop, desktop)
	stopDesktop := desktop.startStoppable(t)
	eventually(t, "desktop online", func() bool { return online(t, laptop, desktop.n.id.KeyID) })
	// 在线状态先于对方功能写进数据库：等台式机记下笔记本支持 leave。
	eventually(t, "laptop features recorded", func() bool {
		known, _ := desktop.n.Store().PeerFeatures()
		for _, f := range known[laptop.n.id.KeyID] {
			if f == FeatureLeave {
				return true
			}
		}
		return false
	})
	oldKey := currentKey(laptop)

	by, err := desktop.n.RequestLeave(t.Context())
	if err != nil {
		t.Fatalf("RequestLeave: %v", err)
	}
	if by != "笔记本" {
		t.Fatalf("removed by %q, want 笔记本", by)
	}
	if laptop.n.isMember(desktop.n.id.KeyID) {
		t.Fatal("laptop still lists the desktop as a member")
	}
	if bytes.Equal(currentKey(laptop), oldKey) {
		t.Fatal("leaving must rotate the discovery key")
	}
	eventually(t, "laptop dropped the desktop", func() bool { return !online(t, laptop, desktop.n.id.KeyID) })

	stopDesktop()
	desktop.reopen(t)
	if err := desktop.n.LeaveCircle(); err != nil {
		t.Fatalf("LeaveCircle: %v", err)
	}
	phone.start(t)
	joinCircle(t, phone, desktop)
	if desktop.n.CircleID() != phone.n.CircleID() {
		t.Fatal("desktop did not join the phone's circle")
	}
	desktop.start(t)
	eventually(t, "desktop online with phone", func() bool { return online(t, phone, desktop.n.id.KeyID) })
}

// 没有在线成员（或在线的都是不认识 LeaveRequest 的旧版本）：ErrNoLeaveHelper，什么都不改。
func TestRequestLeaveWithoutHelper(t *testing.T) {
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	stopLaptop := laptop.startStoppable(t)
	joinCircle(t, laptop, desktop)
	desktop.start(t)
	eventually(t, "desktop online", func() bool { return online(t, laptop, desktop.n.id.KeyID) })

	// 笔记本是旧版本：只声明了 rename。
	if err := desktop.n.Store().SetPeerFeatures(laptop.n.id.KeyID, []string{FeatureRename}); err != nil {
		t.Fatal(err)
	}
	if _, err := desktop.n.RequestLeave(t.Context()); !errors.Is(err, ErrNoLeaveHelper) {
		t.Fatalf("old peer: err = %v, want ErrNoLeaveHelper", err)
	}

	// 笔记本离线。
	stopLaptop()
	eventually(t, "laptop offline", func() bool { return !online(t, desktop, laptop.n.id.KeyID) })
	if err := desktop.n.Store().SetPeerFeatures(laptop.n.id.KeyID, []string{FeatureRename, FeatureLeave}); err != nil {
		t.Fatal(err)
	}
	if _, err := desktop.n.RequestLeave(t.Context()); !errors.Is(err, ErrNoLeaveHelper) {
		t.Fatalf("offline peer: err = %v, want ErrNoLeaveHelper", err)
	}
	if !desktop.n.InCircle() || !desktop.n.isMember(desktop.n.id.KeyID) {
		t.Fatal("a failed RequestLeave must not change the circle")
	}
}

func TestLeaveCircleRefusesWhileDaemonRuns(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	if err := laptop.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	laptop.start(t)
	if err := laptop.n.LeaveCircle(); err == nil {
		t.Fatal("LeaveCircle succeeded while the daemon was running")
	}
	if !laptop.n.InCircle() {
		t.Fatal("left the circle while the daemon was running")
	}
}

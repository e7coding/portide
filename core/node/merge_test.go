package node

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"portide/core/keystore"
	"portide/core/pairing"
	"portide/core/store"
)

// circleOf 让第一台设备建圈并启动守护进程，其余依次用配对码加入（不启动）。返回停止第一台守护进程的函数。
func circleOf(t *testing.T, devs ...*testDevice) (stop func()) {
	t.Helper()
	if err := devs[0].n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	stop = devs[0].startStoppable(t)
	for _, d := range devs[1:] {
		joinCircle(t, devs[0], d)
	}
	return stop
}

func keyIDs(devs ...*testDevice) []string {
	var out []string
	for _, d := range devs {
		out = append(out, d.n.id.KeyID)
	}
	return out
}

func oldDiscoveryKey(t *testing.T, d *testDevice) []byte {
	t.Helper()
	k, err := d.keys.Get(keystore.OldDiscoveryKey)
	if err != nil && !errors.Is(err, keystore.ErrNotFound) {
		t.Fatal(err)
	}
	return k
}

// mergeFixture 是合并之前的两个圈：A = {X, A1, A2}，B = {Y, B1}。
type mergeFixture struct {
	x, a1, a2, y, b1 *testDevice
	oldCircle        string // 合并前 A 的 ID
	oldKey           []byte // 合并前 X 的发现密钥
}

// newMergeFixture 建好 A 和 B；xRuns 为 false 时 X 的守护进程已经停掉（Node 重新打开过），只有 Y 在运行。
func newMergeFixture(t *testing.T, xRuns bool) *mergeFixture {
	t.Helper()
	f := &mergeFixture{
		x: newDevice(t, "X"), a1: newDevice(t, "A1"), a2: newDevice(t, "A2"),
		y: newDevice(t, "Y"), b1: newDevice(t, "B1"),
	}
	stopX := circleOf(t, f.x, f.a1, f.a2)
	if !xRuns {
		stopX()
		f.x.reopen(t)
	}
	circleOf(t, f.y, f.b1)
	f.oldCircle, f.oldKey = f.x.n.CircleID(), currentKey(f.x)
	return f
}

// merge 让 X 输入 Y 的配对码并同意合并。
func (f *mergeFixture) merge(t *testing.T) (*JoinResult, int64) {
	t.Helper()
	code, sid := invite(t, f.y)
	res, err := f.x.n.Join(context.Background(), code, []string{f.y.n.Addr()}, true)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	return res, sid
}

// A = {X, A1, A2}，B = {Y, B1}；只有 Y 在运行。X 输入 Y 的配对码（mergeOK）。
func TestMergeTwoCircles(t *testing.T) {
	f := newMergeFixture(t, false)
	before := time.Now()
	res, sid := f.merge(t)

	if res.Mode != pairing.ModeMerge || res.Peer != "Y" {
		t.Fatalf("result = %+v", res)
	}
	if f.x.n.CircleID() != f.y.n.CircleID() || res.CircleID != f.y.n.CircleID() {
		t.Fatal("X must now be in Y's circle")
	}
	if got := len(mustDevices(t, f.x)); got != 5 || len(res.Members) != 5 {
		t.Fatalf("members = %d / %v, want 5", got, res.Members)
	}
	if !slices.Equal(res.Waiting, []string{"A1", "A2"}) {
		t.Fatalf("Waiting = %v", res.Waiting)
	}

	rec, err := f.x.n.Store().CircleMove()
	if err != nil || rec == nil {
		t.Fatalf("CircleMove = %v, %v", rec, err)
	}
	want := keyIDs(f.a1, f.a2)
	if rec.OldCircle != f.oldCircle || !slices.Equal(rec.Pending, want) || !slices.Equal(rec.Brought, want) {
		t.Fatalf("move record = %+v", rec)
	}
	if len(rec.OldChain) == 0 {
		t.Fatal("the move record must keep A's chain")
	}
	if exp := before.Add(moveGrace); rec.ExpiresAt.Before(exp.Add(-time.Minute)) || rec.ExpiresAt.After(exp.Add(time.Minute)) {
		t.Fatalf("ExpiresAt = %v, want about %v", rec.ExpiresAt, exp)
	}
	if !bytes.Equal(oldDiscoveryKey(t, f.x), f.oldKey) {
		t.Fatal("OldDiscoveryKey must hold A's discovery key")
	}
	if !bytes.Equal(currentKey(f.x), currentKey(f.y)) {
		t.Fatal("X must use B's discovery key")
	}
	if _, k := f.x.n.activeMove(); !bytes.Equal(k, f.oldKey) {
		t.Fatal("activeMove must return A's key")
	}
	// 重新打开之后从数据库和密钥库读回来。
	f.x.reopen(t)
	if r, k := f.x.n.activeMove(); r == nil || !bytes.Equal(k, f.oldKey) || !slices.Equal(r.Pending, want) {
		t.Fatalf("activeMove after reopen = %+v, %x", r, k)
	}

	eventually(t, "Y records the finished session", func() bool {
		ps, _ := f.y.n.Store().GetPairSession(sid)
		return ps.Status == store.PairDone
	})
	ps, _ := f.y.n.Store().GetPairSession(sid)
	if ps.Mode != string(pairing.ModeMerge) || ps.Brought != 2 || ps.JoinerName != "X" {
		t.Fatalf("Y's pair session = %+v", ps)
	}
}

// X 的守护进程在运行：合并之后 X 断开 A 的连接，并在一秒内连上 Y；A1 跟过来之后才再连上。
func TestMergeWhileJoinerRuns(t *testing.T) {
	f := newMergeFixture(t, true)
	f.a1.start(t)
	eventually(t, "X sees A1 online", func() bool { return online(t, f.x, f.a1.n.id.KeyID) })

	f.merge(t)
	start := time.Now()
	eventually(t, "X connects to Y", func() bool { return online(t, f.x, f.y.n.id.KeyID) })
	if d := time.Since(start); d > time.Second {
		t.Fatalf("X took %v to connect to Y", d)
	}
	// 断开之后 A1 用 A 的身份连不回来，只能跟过来（旧圈连接，见 move_test.go）、以 B 成员的身份再连上。
	eventually(t, "A1 back online on X as a member of B", func() bool {
		return online(t, f.x, f.a1.n.id.KeyID) && f.a1.n.CircleID() == f.y.n.CircleID()
	})
}

// 合并之后 X 又单机了（在 B 里把别的设备都移除），再 JOIN 到 C：迁移记录被删、OldDiscoveryKey 为空。
// LeaveCircle 同样清掉。
func TestSwitchCircleClearsMove(t *testing.T) {
	assertCleared := func(t *testing.T, d *testDevice) {
		t.Helper()
		if rec, err := d.n.Store().CircleMove(); err != nil || rec != nil {
			t.Fatalf("CircleMove = %+v, %v; want none", rec, err)
		}
		if k := oldDiscoveryKey(t, d); len(k) != 0 {
			t.Fatalf("OldDiscoveryKey = %x, want empty", k)
		}
		if r, k := d.n.activeMove(); r != nil || k != nil {
			t.Fatalf("activeMove = %+v, %x", r, k)
		}
	}

	t.Run("join", func(t *testing.T) {
		f := newMergeFixture(t, false)
		f.merge(t)
		for _, d := range []*testDevice{f.y, f.b1, f.a1, f.a2} {
			err := f.x.n.RemoveDevice(d.n.id.KeyID)
			if err != nil && !errors.Is(err, ErrRemovedKeyPending) {
				t.Fatalf("RemoveDevice: %v", err)
			}
		}
		if got := len(mustDevices(t, f.x)); got != 1 {
			t.Fatalf("X should be alone, has %d devices", got)
		}
		c := newDevice(t, "C")
		if err := c.n.EnsureCircle(); err != nil {
			t.Fatal(err)
		}
		c.start(t)
		joinCircle(t, c, f.x)
		if f.x.n.CircleID() != c.n.CircleID() {
			t.Fatal("X must be in C's circle")
		}
		assertCleared(t, f.x)
	})

	t.Run("leave", func(t *testing.T) {
		f := newMergeFixture(t, false)
		f.merge(t)
		if err := f.x.n.LeaveCircle(); err != nil {
			t.Fatal(err)
		}
		assertCleared(t, f.x)
	})
}

// 双方都多台、Y 是 M5a：X 得到 pairing.ErrMergeUnsupported，两边的链都没变；Y 的配对码仍有效（不算输错）。
func TestMergeRefusedByOldInviter(t *testing.T) {
	x, a1 := newDevice(t, "X"), newDevice(t, "A1")
	y, b1 := newDevice(t, "Y"), newDevice(t, "B1")
	stopX := circleOf(t, x, a1)
	stopX()
	x.reopen(t)
	y.n.features = []string{FeatureRename, FeatureLeave, pairing.FeatureCircle2}
	circleOf(t, y, b1)
	xBefore, _ := x.n.snapshot()
	yBefore, _ := y.n.snapshot()

	code, sid := invite(t, y)
	_, err := x.n.Join(context.Background(), code, []string{y.n.Addr()}, true)
	if !errors.Is(err, pairing.ErrMergeUnsupported) {
		t.Fatalf("Join err = %v, want ErrMergeUnsupported", err)
	}
	xAfter, _ := x.n.snapshot()
	yAfter, _ := y.n.snapshot()
	if !slices.EqualFunc(xBefore.Records(), xAfter.Records(), bytes.Equal) ||
		!slices.EqualFunc(yBefore.Records(), yAfter.Records(), bytes.Equal) {
		t.Fatal("neither chain may change")
	}
	if rec, _ := x.n.Store().CircleMove(); rec != nil {
		t.Fatal("no move record on a refused merge")
	}
	// 配对码仍然有效：一台单机设备还能用它加入。
	solo := newDevice(t, "solo")
	if _, err := solo.n.Join(context.Background(), code, []string{y.n.Addr()}, false); err != nil {
		t.Fatalf("the code must still work: %v", err)
	}
	eventually(t, "session done", func() bool {
		ps, _ := y.n.Store().GetPairSession(sid)
		return ps.Status == store.PairDone && ps.Attempts == 0
	})
}

// 迁移记录是别的进程（命令行的 portide join）写的：本机的 Node 重新加载链、发现换了圈时，也要重新读迁移记录和旧密钥，
// 两个方向都是——别的进程合并之后出现，别的进程再换圈（传 nil）之后消失。
func TestMoveReloadedAfterOtherProcessSwitches(t *testing.T) {
	f := newMergeFixture(t, false)
	cli, err := Open(Options{DataDir: f.x.dir, Keys: f.x.keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })

	code, _ := invite(t, f.y)
	if _, err := cli.Join(context.Background(), code, []string{f.y.n.Addr()}, true); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if r, k := f.x.n.activeMove(); r != nil || k != nil {
		t.Fatal("precondition: the first Node has not reloaded yet")
	}
	if _, err := f.x.n.reloadCircle(); err != nil {
		t.Fatal(err)
	}
	if r, k := f.x.n.activeMove(); r == nil || r.OldCircle != f.oldCircle || !bytes.Equal(k, f.oldKey) {
		t.Fatalf("after another process merged: activeMove = %+v, %x", r, k)
	}

	// 别的进程把本机变回单机、再加入 C（switchCircle 传 nil）。
	for _, d := range []*testDevice{f.y, f.b1, f.a1, f.a2} {
		if err := cli.RemoveDevice(d.n.id.KeyID); err != nil && !errors.Is(err, ErrRemovedKeyPending) {
			t.Fatalf("RemoveDevice: %v", err)
		}
	}
	c := newDevice(t, "C")
	c.start(t)
	code, _ = invite(t, c)
	if _, err := cli.Join(context.Background(), code, []string{c.n.Addr()}, false); err != nil {
		t.Fatalf("Join C: %v", err)
	}
	if _, err := f.x.n.reloadCircle(); err != nil {
		t.Fatal(err)
	}
	if f.x.n.CircleID() != c.n.CircleID() {
		t.Fatal("the first Node must have reloaded C's chain")
	}
	if r, k := f.x.n.activeMove(); r != nil || k != nil {
		t.Fatalf("after another process switched again: activeMove = %+v, %x", r, k)
	}
}

// 没有迁移记录时换圈（JOIN）不去写密钥库里的旧密钥。
func TestSwitchWithoutMoveSkipsKeystoreWrite(t *testing.T) {
	inviter, joiner := newDevice(t, "邀请方"), newDevice(t, "加入方")
	if err := inviter.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	inviter.start(t)
	joinCircle(t, inviter, joiner)
	if _, err := joiner.keys.Get(keystore.OldDiscoveryKey); !errors.Is(err, keystore.ErrNotFound) {
		t.Fatalf("OldDiscoveryKey must not have been written: %v", err)
	}
}

// 合并时 A 里有设备声明的功能里没有 merge：JoinResult.Outdated 按设备判断，只含那台设备的名字。
func TestMergeOutdatedByDevice(t *testing.T) {
	f := newMergeFixture(t, false)
	if err := f.x.n.Store().SetPeerFeatures(f.a2.n.id.KeyID, []string{FeatureRename}); err != nil {
		t.Fatal(err)
	}
	res, _ := f.merge(t)
	if !slices.Equal(res.Waiting, []string{"A1", "A2"}) || !slices.Equal(res.Outdated, []string{"A2"}) {
		t.Fatalf("Waiting = %v, Outdated = %v", res.Waiting, res.Outdated)
	}
}

// A 里有两台同名设备、只有一台需要升级：Waiting 两个都有，Outdated 里这个名字只出现一次。
func TestMergeOutdatedSameName(t *testing.T) {
	x, p1, p2 := newDevice(t, "X"), newDevice(t, "P"), newDevice(t, "P")
	y, b1 := newDevice(t, "Y"), newDevice(t, "B1")
	stopX := circleOf(t, x, p1, p2)
	stopX()
	x.reopen(t)
	circleOf(t, y, b1)
	if err := x.n.Store().SetPeerFeatures(p2.n.id.KeyID, []string{FeatureRename}); err != nil {
		t.Fatal(err)
	}
	code, _ := invite(t, y)
	res, err := x.n.Join(context.Background(), code, []string{y.n.Addr()}, true)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if !slices.Equal(res.Waiting, []string{"P", "P"}) || !slices.Equal(res.Outdated, []string{"P"}) {
		t.Fatalf("Waiting = %v, Outdated = %v", res.Waiting, res.Outdated)
	}
}

// 同名的两台都需要升级：Outdated 里每台一项，不去重。
func TestMergeOutdatedBothSameName(t *testing.T) {
	x, p1, p2 := newDevice(t, "X"), newDevice(t, "P"), newDevice(t, "P")
	y, b1 := newDevice(t, "Y"), newDevice(t, "B1")
	stopX := circleOf(t, x, p1, p2)
	stopX()
	x.reopen(t)
	circleOf(t, y, b1)
	for _, p := range []*testDevice{p1, p2} {
		if err := x.n.Store().SetPeerFeatures(p.n.id.KeyID, []string{FeatureRename}); err != nil {
			t.Fatal(err)
		}
	}
	code, _ := invite(t, y)
	res, err := x.n.Join(context.Background(), code, []string{y.n.Addr()}, true)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if !slices.Equal(res.Waiting, []string{"P", "P"}) || !slices.Equal(res.Outdated, []string{"P", "P"}) {
		t.Fatalf("Waiting = %v, Outdated = %v", res.Waiting, res.Outdated)
	}
}

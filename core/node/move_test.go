package node

import (
	"bytes"
	"context"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"portide/core/chain"
	"portide/core/keystore"
	"portide/core/memberauth"
	"portide/core/pairing"
	"portide/core/store"
)

// abcFixture 是合并之前的两个圈：A = {X, A1, A2}，B = {Y, B1}。A 的三台监听固定的端口（重新打开之后不变），
// 这样 peers 表里 A 时期的地址之后还能用；五台共用一个内存里的局域网发现，跟过来的设备靠它找到 B 的设备。
type abcFixture struct {
	x, a1, a2, y, b1 *testDevice
	disc             *memDiscovery
	addrs            map[*testDevice]string
	oldCircle        string
	oldKey           []byte
}

// freeAddr 返回一个当前空闲的回环 UDP 地址。
func freeAddr(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}

// setup 给（重新打开的）Node 配上固定的监听地址和共用的发现。
func (f *abcFixture) setup(d *testDevice) {
	if a, ok := f.addrs[d]; ok {
		d.n.opts.ListenAddr = a
	}
	d.n.opts.DisableMDNS = false
	d.n.opts.Discovery = f.disc
}

// reopen 在同一个数据目录上重新打开 d（守护进程运行过的 Node 不能再 Run）。
func (f *abcFixture) reopen(t *testing.T, d *testDevice) {
	t.Helper()
	d.reopen(t)
	f.setup(d)
}

// mergeABC 构造 A 和 B；A 的三台先一起运行过一次（互相记下了地址），返回时都已停下、重新打开过。只有 Y 在运行。
func mergeABC(t *testing.T) *abcFixture {
	t.Helper()
	f := &abcFixture{
		x: newDevice(t, "X"), a1: newDevice(t, "A1"), a2: newDevice(t, "A2"),
		y: newDevice(t, "Y"), b1: newDevice(t, "B1"),
		disc: newMemDiscovery(), addrs: map[*testDevice]string{},
	}
	for _, d := range []*testDevice{f.x, f.a1, f.a2} {
		f.addrs[d] = freeAddr(t)
	}
	for _, d := range []*testDevice{f.x, f.a1, f.a2, f.y, f.b1} {
		f.setup(d)
	}
	stopX := circleOf(t, f.x, f.a1, f.a2)
	stopA1, stopA2 := f.a1.startStoppable(t), f.a2.startStoppable(t)
	eventually(t, "A all online", func() bool {
		return online(t, f.x, f.a1.n.id.KeyID) && online(t, f.x, f.a2.n.id.KeyID) && online(t, f.a1, f.a2.n.id.KeyID)
	})
	stopX()
	stopA1()
	stopA2()
	for _, d := range []*testDevice{f.x, f.a1, f.a2} {
		f.reopen(t, d)
	}
	circleOf(t, f.y, f.b1)
	f.oldCircle, f.oldKey = f.x.n.CircleID(), currentKey(f.x)
	return f
}

// merge 让 X 输入 Y 的配对码并同意合并（X 可以在运行，也可以没在运行）。
func (f *abcFixture) merge(t *testing.T) {
	t.Helper()
	code, _ := invite(t, f.y)
	if _, err := f.x.n.Join(context.Background(), code, []string{f.y.n.Addr()}, true); err != nil {
		t.Fatalf("Join: %v", err)
	}
}

func moveRecord(t *testing.T, d *testDevice) *store.CircleMove {
	t.Helper()
	rec, err := d.n.Store().CircleMove()
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func pendingOf(t *testing.T, d *testDevice) []string {
	t.Helper()
	if rec := moveRecord(t, d); rec != nil {
		return rec.Pending
	}
	return nil
}

func followed(f *abcFixture, d *testDevice) bool {
	return d.n.CircleID() == f.y.n.CircleID()
}

// A1 在线、X 在线：X 合并之后几秒内 A1 跟过来。
func TestMergeOnlineMemberFollows(t *testing.T) {
	f := mergeABC(t)
	f.x.start(t)
	f.a1.start(t)
	eventually(t, "X sees A1 online", func() bool { return online(t, f.x, f.a1.n.id.KeyID) })

	f.merge(t)
	eventually(t, "A1 follows into B", func() bool { return followed(f, f.a1) })
	eventually(t, "A1 and Y online to each other", func() bool {
		return online(t, f.a1, f.y.n.id.KeyID) && online(t, f.y, f.a1.n.id.KeyID)
	})
	eventually(t, "X no longer waits for A1", func() bool {
		return !slices.Contains(pendingOf(t, f.x), f.a1.n.id.KeyID)
	})
	xr, ar := moveRecord(t, f.x), moveRecord(t, f.a1)
	if ar == nil || ar.OldCircle != f.oldCircle || !slices.Equal(ar.Pending, keyIDs(f.a2)) {
		t.Fatalf("A1's move record = %+v", ar)
	}
	if ar.ExpiresAt.Unix() != xr.ExpiresAt.Unix() {
		t.Fatalf("A1's ExpiresAt = %v, X's = %v", ar.ExpiresAt, xr.ExpiresAt)
	}
	if !bytes.Equal(oldDiscoveryKey(t, f.a1), f.oldKey) || !bytes.Equal(currentKey(f.a1), currentKey(f.y)) {
		t.Fatal("A1 must keep A's key as the old key and use B's key")
	}
}

// A2 合并时不在线：之后启动 A2（它按老习惯拨 X 的地址，X 作为接收方走旧圈）：A2 跟过来。
func TestMergeOfflineMemberFollowsLater(t *testing.T) {
	f := mergeABC(t)
	f.x.start(t)
	f.merge(t)
	f.a2.start(t)
	eventually(t, "A2 follows into B", func() bool { return followed(f, f.a2) })
	eventually(t, "A2 online with X as a member of B", func() bool {
		return online(t, f.a2, f.x.n.id.KeyID) && online(t, f.x, f.a2.n.id.KeyID)
	})
	eventually(t, "X no longer waits for A2", func() bool {
		return !slices.Contains(pendingOf(t, f.x), f.a2.n.id.KeyID)
	})
}

// X 合并后关机；A1 已经跟过来；A2 之后上线：经 A1 跟过来（不经 X）。
func TestMoveViaAnotherMigratedDevice(t *testing.T) {
	f := mergeABC(t)
	stopX := f.x.startStoppable(t)
	f.a1.start(t)
	f.merge(t)
	eventually(t, "A1 follows into B", func() bool { return followed(f, f.a1) })
	stopX()

	f.a2.start(t)
	eventually(t, "A2 follows into B through A1", func() bool { return followed(f, f.a2) })
	rec := moveRecord(t, f.a2)
	if rec == nil || !slices.Equal(rec.Pending, keyIDs(f.x)) {
		t.Fatalf("A2 followed A1, so it still waits only for X: %+v", rec)
	}
	eventually(t, "A1 no longer waits for A2", func() bool {
		return !slices.Contains(pendingOf(t, f.a1), f.a2.n.id.KeyID)
	})
}

// A1、A2 都已经迁移、彼此都还在对方的 Pending 里：碰上之后两边都去掉对方，随后以 B 成员身份互相在线，
// 之后不再发起旧圈连接。
func TestTwoHelpersConverge(t *testing.T) {
	f := mergeABC(t)
	f.x.start(t)
	stop1, stop2 := f.a1.startStoppable(t), f.a2.startStoppable(t)
	aChain, _ := f.a1.n.snapshot()
	f.merge(t)
	eventually(t, "both follow", func() bool { return followed(f, f.a1) && followed(f, f.a2) })
	eventually(t, "A1 and A2 online to each other", func() bool { return online(t, f.a1, f.a2.n.id.KeyID) })
	stop1()
	stop2()

	// 把对方放回自己的 Pending 里：A1 和 A2 只能靠旧圈连接互相确认。Pending 空了之后迁移记录和 A 的密钥
	// 已经被删掉（syncMove），这里重新写一份。
	logs := map[*testDevice]*logRecorder{}
	for _, p := range [][2]*testDevice{{f.a1, f.a2}, {f.a2, f.a1}} {
		d, other := p[0], p[1]
		rec := &store.CircleMove{
			OldCircle: f.oldCircle, OldChain: aChain.Records(),
			Brought: keyIDs(other), Pending: keyIDs(other), ExpiresAt: time.Now().Add(moveGrace),
		}
		if err := d.n.Store().SaveCircleMove(rec); err != nil {
			t.Fatal(err)
		}
		if err := d.keys.Set(keystore.OldDiscoveryKey, f.oldKey); err != nil {
			t.Fatal(err)
		}
		f.reopen(t, d)
		logs[d] = &logRecorder{}
		d.n.opts.Logf = logs[d].Logf
	}
	f.a1.start(t)
	f.a2.start(t)
	eventually(t, "both drop each other from Pending", func() bool {
		return len(pendingOf(t, f.a1)) == 0 && len(pendingOf(t, f.a2)) == 0
	})
	eventually(t, "A1 and A2 online as members of B", func() bool {
		return online(t, f.a1, f.a2.n.id.KeyID) && online(t, f.a2, f.a1.n.id.KeyID)
	})
	before := logs[f.a1].count(oldCircleLog()) + logs[f.a2].count(oldCircleLog())
	if before == 0 {
		t.Fatal("they must have met on an old-circle connection")
	}
	time.Sleep(2*dialInterval + 500*time.Millisecond)
	if after := logs[f.a1].count(oldCircleLog()) + logs[f.a2].count(oldCircleLog()); after != before {
		t.Fatalf("old-circle connections kept going: %d → %d", before, after)
	}
}

// A1 声明的功能里没有 merge（旧版本）：X 不发 CircleMove，A1 仍在 Pending 里，moveTried 里有 A1。
func TestMoveSkipsOldVersionMember(t *testing.T) {
	f := mergeABC(t)
	f.a1.n.features = []string{FeatureRename, FeatureLeave, pairing.FeatureCircle2}
	f.merge(t)
	f.a1.start(t)
	f.x.start(t)
	eventually(t, "X tried A1", func() bool {
		d := f.x.n.daemon
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.moveTried[f.a1.n.id.KeyID]
		return ok
	})
	time.Sleep(300 * time.Millisecond)
	if followed(f, f.a1) || f.a1.n.CircleID() != f.oldCircle {
		t.Fatal("A1 must stay in A")
	}
	if !slices.Contains(pendingOf(t, f.x), f.a1.n.id.KeyID) {
		t.Fatal("A1 must still be pending on X")
	}
	feats, _ := f.x.n.Store().PeerFeatures()
	if slices.Contains(feats[f.a1.n.id.KeyID], pairing.FeatureMerge) || len(feats[f.a1.n.id.KeyID]) == 0 {
		t.Fatalf("X must record A1's features: %v", feats[f.a1.n.id.KeyID])
	}
}

// A1 已经跟着 Z 合并到了第三个圈 C（它也是帮手：迁移记录里有 A 的链和 A 的密钥，X 在它的 Pending 里）：
// X 发 CircleMove 得到 "moved elsewhere"，X 的 Elsewhere 含 A1，A1 仍在 C。
func TestMoveConflictThirdCircle(t *testing.T) {
	f := mergeABC(t)
	// A1 先跟着 Z 换到了 C：Z 把 A 的设备都签进了 C = {Z, A1, X, A2}。
	z := newDevice(t, "Z")
	c, err := chain.New(z.n.id, "Z")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*testDevice{f.a1, f.x, f.a2} {
		if err := c.AddDevice(z.n.id, d.n.id.Public, d.n.Name()); err != nil {
			t.Fatal(err)
		}
	}
	aChain, aKey := f.a1.n.snapshot()
	mv := &newMove{
		rec: &store.CircleMove{
			OldCircle: f.oldCircle, OldChain: aChain.Records(),
			Brought: keyIDs(f.x, f.a2), Pending: keyIDs(f.x, f.a2), ExpiresAt: time.Now().Add(moveGrace),
		},
		oldKey: aKey,
	}
	if err := f.a1.n.switchCircle(c.Records(), bytes.Repeat([]byte{7}, 32), "", nil, 0, mv); err != nil {
		t.Fatal(err)
	}
	cID := f.a1.n.CircleID()

	f.merge(t)
	f.a1.start(t)
	f.x.start(t)
	eventually(t, "X records A1 as elsewhere", func() bool {
		rec := moveRecord(t, f.x)
		return rec != nil && slices.Contains(rec.Elsewhere, f.a1.n.id.KeyID)
	})
	if f.a1.n.CircleID() != cID {
		t.Fatal("A1 must stay in C")
	}
	if !slices.Contains(pendingOf(t, f.x), f.a1.n.id.KeyID) {
		t.Fatal("A1 stays pending on X (shown as elsewhere)")
	}
	d := f.x.n.daemon
	d.mu.Lock()
	_, tried := d.moveTried[f.a1.n.id.KeyID]
	d.mu.Unlock()
	if !tried {
		t.Fatal("X must not retry A1 right away")
	}
}

// 不在 Pending 里、或者已经被移出 B 的 A 成员用 A 的密钥连 X：X 拒绝，对方拿不到任何东西。
func TestOldCircleRejectsNonPending(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	// 让 X 只等 A1；A2 从来不在 Pending 里。
	rec := moveRecord(t, f.x)
	rec.Pending = keyIDs(f.a1)
	if err := f.x.n.Store().SaveCircleMove(rec); err != nil {
		t.Fatal(err)
	}
	f.reopen(t, f.x)
	f.x.start(t)
	if f.x.n.Addr() != f.addrs[f.x] {
		t.Fatalf("X listens on %s, want its old address %s", f.x.n.Addr(), f.addrs[f.x])
	}

	// 成员核对本身：A1 在 Pending 里放行，A2 不在，拒绝。
	admit := func(d *testDevice) error {
		c, _ := d.n.snapshot()
		_, err := f.x.n.admitOld(&memberauth.Peer{KeyID: d.n.id.KeyID, Chain: c.Records()})
		return err
	}
	if err := admit(f.a1); err != nil {
		t.Fatalf("A1 is pending: %v", err)
	}
	if err := admit(f.a2); !errors.Is(err, errNotMember) {
		t.Fatalf("A2 is not pending: err = %v, want errNotMember", err)
	}

	// A2 按老习惯用 A 的密钥连 X（每 3 秒一次）：什么都拿不到。
	a2Before, _ := f.a2.n.snapshot()
	f.a2.start(t)
	time.Sleep(dialInterval + 500*time.Millisecond)
	a2After, _ := f.a2.n.snapshot()
	if f.a2.n.CircleID() != f.oldCircle || !slices.EqualFunc(a2Before.Records(), a2After.Records(), bytes.Equal) {
		t.Fatal("A2 must learn nothing")
	}
	if online(t, f.x, f.a2.n.id.KeyID) || online(t, f.a2, f.x.n.id.KeyID) {
		t.Fatal("no link may be registered")
	}

	// A1 在 Pending 里，但已经被移出 B：同样拒绝。
	if err := f.x.n.RemoveDevice(f.a1.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	if err := admit(f.a1); !errors.Is(err, errNotMember) {
		t.Fatalf("A1 was removed from B: err = %v, want errNotMember", err)
	}
	a1Before, _ := f.a1.n.snapshot()
	f.a1.start(t)
	time.Sleep(dialInterval + 500*time.Millisecond)
	a1After, _ := f.a1.n.snapshot()
	if f.a1.n.CircleID() != f.oldCircle || !slices.EqualFunc(a1Before.Records(), a1After.Records(), bytes.Equal) {
		t.Fatal("a device removed from B must not follow")
	}
	if online(t, f.x, f.a1.n.id.KeyID) {
		t.Fatal("no link to the removed device")
	}
}

// 旧版本的 A1 每隔几秒按老习惯用 A 的密钥连 X：第一次之后 X 把它记进冷却期，之后在核对身份时就拒绝，
// 不再完成旧圈连接、不再重复记日志。
func TestOldCirclePeerThrottled(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	f.a1.n.features = []string{FeatureRename, FeatureLeave, pairing.FeatureCircle2}
	logs := &logRecorder{}
	f.x.n.opts.Logf = logs.Logf
	f.x.start(t)
	f.a1.start(t)
	eventually(t, "X tried A1", func() bool {
		d := f.x.n.daemon
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.moveTried[f.a1.n.id.KeyID]
		return ok
	})
	time.Sleep(2*dialInterval + 500*time.Millisecond)
	if got := logs.count(oldCircleLog()); got != 1 {
		t.Fatalf("old-circle exchanges with A1 = %d, want 1 (later attempts must be refused during the cooldown)", got)
	}
	if got := logs.count("版本不支持合并设备圈"); got != 1 {
		t.Fatalf("the outdated-version line was logged %d times, want 1", got)
	}
	if f.a1.n.CircleID() != f.oldCircle {
		t.Fatal("A1 must stay in A")
	}
}

// X 和 A1 记下的 A 链分叉了（合并之后各自又签了一台新设备）：A1 照样跟过来，X 的 B 不受核对影响，日志里有分叉。
func TestMoveForkedOldChainStillMoves(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)

	// X 的迁移记录里的 A 链多了 W1。
	w1, w2 := newDevice(t, "W1"), newDevice(t, "W2")
	rec := moveRecord(t, f.x)
	xa, err := chain.Load(rec.OldChain)
	if err != nil {
		t.Fatal(err)
	}
	if err := xa.AddDevice(f.x.n.id, w1.n.id.Public, "W1"); err != nil {
		t.Fatal(err)
	}
	rec.OldChain = xa.Records()
	if err := f.x.n.Store().SaveCircleMove(rec); err != nil {
		t.Fatal(err)
	}
	f.reopen(t, f.x)
	// A1 自己的 A 链多了 W2。
	a1c, _ := f.a1.n.snapshot()
	a1a, err := chain.Load(a1c.Records())
	if err != nil {
		t.Fatal(err)
	}
	if err := a1a.AddDevice(f.a1.n.id, w2.n.id.Public, "W2"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a1.n.mergeChain(a1a.Records()); err != nil {
		t.Fatal(err)
	}

	xLogs, a1Logs := &logRecorder{}, &logRecorder{}
	f.x.n.opts.Logf, f.a1.n.opts.Logf = xLogs.Logf, a1Logs.Logf
	xBefore, _ := f.x.n.snapshot()
	f.x.start(t)
	f.a1.start(t)
	eventually(t, "A1 follows into B despite the fork", func() bool { return followed(f, f.a1) })
	eventually(t, "the fork is logged", func() bool {
		return xLogs.contains(oldForkLog()) || a1Logs.contains(oldForkLog())
	})
	xAfter, _ := f.x.n.snapshot()
	if !slices.EqualFunc(xBefore.Records(), xAfter.Records(), bytes.Equal) {
		t.Fatal("a forked old chain must not change X's B")
	}
	for _, w := range []*testDevice{w1, w2} {
		if xAfter.IsMember(w.n.id.KeyID) {
			t.Fatalf("%s must not be signed into B", w.n.Name())
		}
	}
	// A1 按自己的 A 链（有 W2、没有 W1）留迁移记录。
	ar := moveRecord(t, f.a1)
	oc, err := chain.Load(ar.OldChain)
	if err != nil || !oc.IsMember(w2.n.id.KeyID) || oc.IsMember(w1.n.id.KeyID) {
		t.Fatalf("A1's old chain must be its own: %v", err)
	}
}

// clearAddrs 清掉 d 的 peers 表里这些设备的地址：之后只能靠局域网发现找到它们。
func clearAddrs(t *testing.T, d *testDevice, devs ...*testDevice) {
	t.Helper()
	for _, o := range devs {
		if err := d.n.Store().UpsertPeer(store.Peer{KeyID: o.n.id.KeyID}); err != nil {
			t.Fatal(err)
		}
	}
}

// 只用假发现器（不给直连地址，peers 表里 A1 的地址清空）：X 用 A 的密钥发布令牌，A1 浏览到之后拨 X，跟过来。
func TestMoveFoundOverMDNS(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	clearAddrs(t, f.x, f.a1, f.a2)
	clearAddrs(t, f.a1, f.x, f.a2)
	f.x.start(t)
	f.a1.start(t)
	eventually(t, "A1 follows into B over mDNS", func() bool { return followed(f, f.a1) })
	eventually(t, "X no longer waits for A1", func() bool {
		return !slices.Contains(pendingOf(t, f.x), f.a1.n.id.KeyID)
	})
}

func moveOf(t *testing.T, d *testDevice, keyID string) DeviceStatus {
	t.Helper()
	devs, err := d.n.Devices()
	if err != nil {
		t.Fatal(err)
	}
	for _, dev := range devs {
		if dev.KeyID == keyID {
			return dev
		}
	}
	t.Fatalf("%s is not a member", keyID)
	return DeviceStatus{}
}

// 把 X 的记录 ExpiresAt 改成过去：一次轮询之后 OldDiscoveryKey 为空、OldChain 为 nil、记录还在；
// X 的 Devices() 里 A2.Move == "left"；activeMove() 的 oldKey 为 nil，不再发起旧圈连接。
func TestMoveExpiry(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	rec := moveRecord(t, f.x)
	rec.ExpiresAt = time.Now().Add(-time.Minute)
	if err := f.x.n.Store().SaveCircleMove(rec); err != nil {
		t.Fatal(err)
	}
	f.reopen(t, f.x)
	logs := &logRecorder{}
	f.x.n.opts.Logf = logs.Logf
	f.x.start(t)
	eventually(t, "the old key and chain are dropped", func() bool {
		r := moveRecord(t, f.x)
		return r != nil && r.OldChain == nil && len(oldDiscoveryKey(t, f.x)) == 0
	})
	if r := moveRecord(t, f.x); len(r.Pending) != 2 {
		t.Fatalf("the record must stay with its Pending: %+v", r)
	}
	if got := moveOf(t, f.x, f.a2.n.id.KeyID).Move; got != MoveLeft {
		t.Fatalf("A2.Move = %q, want %q", got, MoveLeft)
	}
	if _, oldKey := f.x.n.activeMove(); oldKey != nil {
		t.Fatal("an expired move must have no old key")
	}
	f.a1.start(t)
	time.Sleep(dialInterval + 500*time.Millisecond)
	if logs.count(oldCircleLog()) != 0 || f.a1.n.CircleID() != f.oldCircle {
		t.Fatal("no old-circle connection after expiry")
	}
}

// 在 B 里移除还在 Pending 里的 A2：一次轮询之后 Pending 不含 A2；Pending 空了之后记录被删。
func TestMovePendingDropsRemovedDevice(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	f.x.start(t)
	if err := f.x.n.RemoveDevice(f.a2.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "A2 is no longer pending", func() bool {
		p := pendingOf(t, f.x)
		return slices.Equal(p, keyIDs(f.a1))
	})
	if err := f.x.n.RemoveDevice(f.a1.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the record and the old key are gone", func() bool {
		return moveRecord(t, f.x) == nil && len(oldDiscoveryKey(t, f.x)) == 0
	})
	if rec, _ := f.x.n.activeMove(); rec != nil {
		t.Fatal("the cached record must be gone too")
	}
}

// Devices() 的 Move 字段：waiting、outdated（SetPeerFeatures 只给 rename）、elsewhere、left 各一台；B 原有的设备和已跟过来的为空。
func TestDevicesMoveStatus(t *testing.T) {
	x, a1, a2, a3, a4 := newDevice(t, "X"), newDevice(t, "A1"), newDevice(t, "A2"), newDevice(t, "A3"), newDevice(t, "A4")
	y, b1 := newDevice(t, "Y"), newDevice(t, "B1")
	stopX := circleOf(t, x, a1, a2, a3, a4)
	stopX()
	x.reopen(t)
	circleOf(t, y, b1)
	code, _ := invite(t, y)
	if _, err := x.n.Join(context.Background(), code, []string{y.n.Addr()}, true); err != nil {
		t.Fatalf("Join: %v", err)
	}
	rec := moveRecord(t, x)
	rec.Pending = keyIDs(a1, a2, a3) // A4 已经跟过来
	rec.Elsewhere = keyIDs(a3)
	if err := x.n.Store().SaveCircleMove(rec); err != nil {
		t.Fatal(err)
	}
	if err := x.n.Store().SetPeerFeatures(a2.n.id.KeyID, []string{FeatureRename}); err != nil {
		t.Fatal(err)
	}
	check := func(want map[*testDevice]string) {
		t.Helper()
		for d, w := range want {
			got := moveOf(t, x, d.n.id.KeyID)
			if got.Move != w {
				t.Errorf("%s.Move = %q, want %q", d.n.Name(), got.Move, w)
			}
			wantUntil := time.Time{}
			if w != "" {
				wantUntil = rec.ExpiresAt
			}
			if !got.MoveUntil.Equal(wantUntil) {
				t.Errorf("%s.MoveUntil = %v, want %v", d.n.Name(), got.MoveUntil, wantUntil)
			}
		}
	}
	check(map[*testDevice]string{
		a1: MoveWaiting, a2: MoveOutdated, a3: MoveElsewhere, a4: "", x: "", y: "", b1: "",
	})

	rec.ExpiresAt = time.Now().Add(-time.Minute)
	if err := x.n.Store().SaveCircleMove(rec); err != nil {
		t.Fatal(err)
	}
	rec = moveRecord(t, x)
	check(map[*testDevice]string{
		a1: MoveLeft, a2: MoveLeft, a3: MoveElsewhere, a4: "", y: "",
	})
}

// addToOldCircle 让还在 A 里的 d 在自己的 A 链上签入一台新设备 w（d 没在运行）。
func addToOldCircle(t *testing.T, d, w *testDevice) {
	t.Helper()
	c, _ := d.n.snapshot()
	next, err := chain.Load(c.Records())
	if err != nil {
		t.Fatal(err)
	}
	if err := next.AddDevice(d.n.id, w.n.id.Public, w.n.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.n.mergeChain(next.Records()); err != nil {
		t.Fatal(err)
	}
}

// 合并之后，还在 A 里的 A1（没跟过来）又加了 W；A1 跟过来时：W 被 X 签进 B、在 X 的 Pending 里；A1 的记录里也有 W。
func TestReconcileBringsDeviceAddedToOldCircle(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	w := newDevice(t, "W")
	addToOldCircle(t, f.a1, w)

	f.x.start(t)
	f.a1.start(t)
	eventually(t, "A1 follows into B", func() bool { return followed(f, f.a1) })
	xb, _ := f.x.n.snapshot()
	m, ok := xb.Member(w.n.id.KeyID)
	if !ok || m.Name != "W" {
		t.Fatalf("W must be signed into B under its name in A: %+v %v", m, ok)
	}
	xr := moveRecord(t, f.x)
	if xr == nil || !slices.Contains(xr.Pending, w.n.id.KeyID) || !slices.Contains(xr.Brought, w.n.id.KeyID) {
		t.Fatalf("X must wait for W: %+v", xr)
	}
	a1b, _ := f.a1.n.snapshot()
	if !a1b.IsMember(w.n.id.KeyID) {
		t.Fatal("the CircleMove A1 got must carry the reconciled B")
	}
	ar := moveRecord(t, f.a1)
	if ar == nil || !slices.Contains(ar.Pending, w.n.id.KeyID) || !slices.Contains(ar.Brought, w.n.id.KeyID) {
		t.Fatalf("A1 must wait for W too: %+v", ar)
	}
}

// 合并之后 A1 在 A 里移除了 A2（A2 已经被签进 B、还没跟过来）：A1 跟过来时 A2 被移出 B，B 的发现密钥换了，X 的 Pending/Brought 都不含 A2。
func TestReconcileRemovesDeviceRemovedFromOldCircle(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	bKey := currentKey(f.x)
	if err := f.a1.n.RemoveDevice(f.a2.n.id.KeyID); err != nil {
		t.Fatal(err)
	}

	f.x.start(t)
	f.a1.start(t)
	eventually(t, "A1 follows into B", func() bool { return followed(f, f.a1) })
	xb, _ := f.x.n.snapshot()
	if xb.IsMember(f.a2.n.id.KeyID) {
		t.Fatal("A2 must be removed from B")
	}
	if _, ok := xb.FormerMember(f.a2.n.id.KeyID); !ok {
		t.Fatal("B must record the removal of A2")
	}
	if bytes.Equal(currentKey(f.x), bKey) {
		t.Fatal("removing A2 must rotate B's discovery key")
	}
	if !bytes.Equal(currentKey(f.a1), currentKey(f.x)) {
		t.Fatal("the CircleMove A1 got must carry B's new key")
	}
	if a1b, _ := f.a1.n.snapshot(); a1b.IsMember(f.a2.n.id.KeyID) {
		t.Fatal("the CircleMove A1 got must carry the reconciled B")
	}
	// A1 跟过来之后 X 的 Pending 空了，记录可能已经被删掉；还在的话不能含 A2。
	if xr := moveRecord(t, f.x); xr != nil &&
		(slices.Contains(xr.Pending, f.a2.n.id.KeyID) || slices.Contains(xr.Brought, f.a2.n.id.KeyID)) {
		t.Fatalf("X must drop A2: %+v", xr)
	}
}

// A1 已经跟过来之后，还在 A 里的 A2 在 A 里移除了 A1；X 碰上 A2 时，A1 仍是 B 的成员，B 的发现密钥不变：
// A 里的移除只对还没跟过来的设备生效。
func TestReconcileKeepsMovedDeviceRemovedFromOldCircle(t *testing.T) {
	f := mergeABC(t)
	stopX := f.x.startStoppable(t)
	stopA1 := f.a1.startStoppable(t)
	f.merge(t)
	eventually(t, "A1 follows into B", func() bool { return followed(f, f.a1) })
	eventually(t, "X no longer waits for A1", func() bool {
		return slices.Equal(pendingOf(t, f.x), keyIDs(f.a2))
	})
	stopX()
	stopA1()
	f.reopen(t, f.x)
	if err := f.a2.n.RemoveDevice(f.a1.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	bKey := currentKey(f.x)

	f.x.start(t)
	f.a2.start(t)
	eventually(t, "A2 follows into B", func() bool { return followed(f, f.a2) })
	xb, _ := f.x.n.snapshot()
	if !xb.IsMember(f.a1.n.id.KeyID) {
		t.Fatal("A1 already moved into B and must stay a member")
	}
	if !bytes.Equal(currentKey(f.x), bKey) {
		t.Fatal("B's discovery key must not change")
	}
	if a2b, _ := f.a2.n.snapshot(); !a2b.IsMember(f.a1.n.id.KeyID) {
		t.Fatal("the CircleMove A2 got must keep A1 in B")
	}
}

// 迁移消息里的期限超过从现在起的 30 天：接收方把它截到 30 天。
func TestFollowerCapsMoveExpiry(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	m := f.x.n.circleMoveFor(f.a1.n.id.KeyID)
	if m == nil {
		t.Fatal("X must have a CircleMove for A1")
	}
	m.ExpiresAt = time.Now().Add(10 * moveGrace).Unix()
	if reply, _ := f.a1.n.acceptCircleMove(streamPeer{KeyID: f.x.n.id.KeyID, Name: "X"}, m); reply != "" {
		t.Fatalf("acceptCircleMove: %q", reply)
	}
	ar := moveRecord(t, f.a1)
	if ar == nil || ar.ExpiresAt.After(time.Now().Add(moveGrace)) {
		t.Fatalf("A1's ExpiresAt must be capped to 30 days from now: %+v", ar)
	}
}

// X 和 A1 之间没有局域网这条路（关闭局域网发现、peers 表里的地址清空），两边用同一个中继：
// 合并之后 A1 经中继走旧圈连接跟过来。
func TestMoveOverRelay(t *testing.T) {
	cfg := startTestRelay(t)
	f := mergeABC(t)
	f.merge(t)
	clearAddrs(t, f.x, f.a1, f.a2)
	clearAddrs(t, f.a1, f.x, f.a2)
	logs := &logRecorder{}
	f.x.n.opts.Logf = logs.Logf
	for _, d := range []*testDevice{f.x, f.a1} {
		d.n.opts.DisableMDNS = true
		d.n.opts.RelayAfter = 200 * time.Millisecond
		d.n.candidatesHook = noDirect // 留在中继上：本机回环上打洞必然成功
		useRelay(t, d, cfg)
	}
	f.x.start(t)
	f.a1.start(t)
	eventually(t, "A1 follows into B over the relay", func() bool { return followed(f, f.a1) })
	if !logs.contains(oldCircleLog() + "「A1」（经中继）") {
		t.Fatal("the old-circle connection must go through the relay")
	}
	eventually(t, "X no longer waits for A1", func() bool {
		return !slices.Contains(pendingOf(t, f.x), f.a1.n.id.KeyID)
	})
	eventually(t, "X and A1 online as members of B", func() bool {
		return online(t, f.x, f.a1.n.id.KeyID) && online(t, f.a1, f.x.n.id.KeyID)
	})
}

// 合并前 X 和 A1 都配了 R_A，Y 配了 R_B：合并后 X 的列表是 [R_B, R_A]，仍在 R_A 上登记，
// 只认 R_A 的 A1 经它跟过来，之后也是 [R_B, R_A]（中继设置同步设计第 5 节）。
func TestMoveOverOldCircleRelay(t *testing.T) {
	ra, rb := startTestRelay(t), startTestRelay(t)
	f := mergeABC(t)
	useRelay(t, f.x, ra)
	useRelay(t, f.a1, ra)
	useRelay(t, f.y, rb)
	f.merge(t)
	if !slices.Equal(relayStrings(f.x), configStrings(rb, ra)) {
		t.Fatalf("X relays after merge = %v, want [R_B, R_A]", relayStrings(f.x))
	}
	clearAddrs(t, f.x, f.a1, f.a2)
	clearAddrs(t, f.a1, f.x, f.a2)
	logs := &logRecorder{}
	f.x.n.opts.Logf = logs.Logf
	for _, d := range []*testDevice{f.x, f.a1} {
		d.n.opts.DisableMDNS = true
		d.n.opts.RelayAfter = 200 * time.Millisecond
		d.n.candidatesHook = noDirect // 留在中继上：本机回环上打洞必然成功
	}
	f.x.start(t)
	f.a1.start(t)
	eventually(t, "A1 follows into B over R_A", func() bool { return followed(f, f.a1) })
	if !logs.contains(oldCircleLog() + "「A1」（经中继）") {
		t.Fatal("the old-circle connection must go through the relay")
	}
	waitRelays(t, "A1 relays become [R_B, R_A]", f.a1, rb, ra)
}

// 合并之后用户在 B 里移除了 A2，A2 仍在 A 里：A1 跟过来时 A2 不会被重新签进 B。
func TestReconcileDoesNotReaddRemovedFromNewCircle(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	if err := f.x.n.RemoveDevice(f.a2.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	// Brought 里也去掉 A2：只靠 B 里的移除记录挡住它。
	rec := moveRecord(t, f.x)
	rec.Brought = keyIDs(f.a1)
	rec.Pending = keyIDs(f.a1)
	if err := f.x.n.Store().SaveCircleMove(rec); err != nil {
		t.Fatal(err)
	}
	f.reopen(t, f.x)
	xBefore, _ := f.x.n.snapshot()

	f.x.start(t)
	f.a1.start(t)
	eventually(t, "A1 follows into B", func() bool { return followed(f, f.a1) })
	xAfter, _ := f.x.n.snapshot()
	if xAfter.IsMember(f.a2.n.id.KeyID) {
		t.Fatal("A2 was removed from B and must not be signed back in")
	}
	if !slices.EqualFunc(xBefore.Records(), xAfter.Records(), bytes.Equal) {
		t.Fatal("reconciling must not change B")
	}
}

// X 的 A 链和 A1 的 A 链分叉（两边各有一条加入记录）：不核对，B 不变，A1 照常跟过来，日志里有"分叉"。
func TestReconcileSkipsForkedOldChain(t *testing.T) {
	f := mergeABC(t)
	f.merge(t)
	// X 的迁移记录里的 A 链多了 W1：要是核对了，W1 会被签进 B。
	w1, w2 := newDevice(t, "W1"), newDevice(t, "W2")
	rec := moveRecord(t, f.x)
	xa, err := chain.Load(rec.OldChain)
	if err != nil {
		t.Fatal(err)
	}
	if err := xa.AddDevice(f.x.n.id, w1.n.id.Public, "W1"); err != nil {
		t.Fatal(err)
	}
	rec.OldChain = xa.Records()
	if err := f.x.n.Store().SaveCircleMove(rec); err != nil {
		t.Fatal(err)
	}
	f.reopen(t, f.x)
	// A1 自己的 A 链多了 W2。
	addToOldCircle(t, f.a1, w2)

	logs := &logRecorder{}
	f.x.n.opts.Logf = logs.Logf
	xBefore, _ := f.x.n.snapshot()
	f.x.start(t)
	f.a1.start(t)
	eventually(t, "A1 follows into B despite the fork", func() bool { return followed(f, f.a1) })
	eventually(t, "X logs the fork", func() bool { return logs.contains(oldForkLog()) })
	xAfter, _ := f.x.n.snapshot()
	if !slices.EqualFunc(xBefore.Records(), xAfter.Records(), bytes.Equal) {
		t.Fatal("a forked old chain must not change X's B")
	}
	if xr := moveRecord(t, f.x); xr != nil && (slices.Contains(xr.Pending, w1.n.id.KeyID) || slices.Contains(xr.Pending, w2.n.id.KeyID)) {
		t.Fatalf("no new device may become pending on a fork: %+v", xr)
	}
}

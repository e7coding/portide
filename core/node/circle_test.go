package node

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"portide/core/chain"
	"portide/core/pairing"
	"portide/core/store"
)

// adoptWhileRunning 搭出反向加入的场景：电脑和 Windows 组成圈 A（都在运行、互相在线），
// 手机单机运行、开放配对；电脑用手机的配对码 Join。返回手机原来的单机圈 ID 和配对会话 ID。
func adoptWhileRunning(t *testing.T) (pc, win, phone *testDevice, res *JoinResult, soloID string, sid int64) {
	t.Helper()
	pc, win, phone = newDevice(t, "电脑"), newDevice(t, "Windows"), newDevice(t, "手机")
	pc.start(t)
	joinCircle(t, pc, win)
	win.start(t)
	eventually(t, "pc and windows online", func() bool { return online(t, pc, win.n.id.KeyID) })

	phone.start(t)
	code, sid := invite(t, phone)
	soloID = phone.n.CircleID()
	res, err := pc.n.Join(t.Context(), code, []string{phone.n.Addr()}, false)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	return pc, win, phone, res, soloID, sid
}

// 本机在多台设备的圈里、对方单机：对方换到本机的圈（反向加入），双方都在运行，不用重启。
func TestJoinWhileRunningSwitchesCircle(t *testing.T) {
	pc, win, phone, res, _, _ := adoptWhileRunning(t)
	if res.Mode != pairing.ModeAdopt || res.Peer != "手机" {
		t.Fatalf("join result = %+v, want adopt by 手机", res)
	}
	if res.CircleID != pc.n.CircleID() || len(res.Members) != 3 {
		t.Fatalf("join result = %+v, want pc's circle with 3 members", res)
	}
	eventually(t, "phone switched to circle A", func() bool { return phone.n.CircleID() == pc.n.CircleID() })
	eventually(t, "pc and phone online", func() bool {
		return online(t, pc, phone.n.id.KeyID) && online(t, phone, pc.n.id.KeyID)
	})
	// 新链推送给了 Windows（测试关闭了 mDNS，手机和 Windows 之间没有已知地址，不要求它们互相在线）。
	eventually(t, "windows learns about the phone", func() bool {
		devs, _ := win.n.Devices()
		return slices.ContainsFunc(devs, func(d DeviceStatus) bool { return d.KeyID == phone.n.id.KeyID })
	})

	if _, err := pc.n.QueueSend(phone.n.id.KeyID, plan(t, "换圈之后的第一条", nil)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone receives the text", func() bool {
		got, _ := phone.n.History("", 10)
		return len(got) == 1 && len(got[0].Body.Texts) == 1 && got[0].Body.Texts[0] == "换圈之后的第一条"
	})
}

// 邀请方（手机）那一侧：配对会话记为反向加入完成（不是被换圈作废），原来的单机圈不再使用。
func TestAdoptWhileInviterRuns(t *testing.T) {
	_, _, phone, _, soloID, sid := adoptWhileRunning(t)
	eventually(t, "phone records the adopt", func() bool {
		ps, _ := phone.n.Store().GetPairSession(sid)
		return ps.Status == store.PairDone
	})
	ps, _ := phone.n.Store().GetPairSession(sid)
	if ps.Mode != string(pairing.ModeAdopt) || ps.JoinerName != "电脑" || ps.Error != "" {
		t.Fatalf("phone's session = %+v, want done/adopt by 电脑", ps)
	}
	if phone.n.CircleID() == soloID {
		t.Fatal("phone still uses its solo circle")
	}
}

// 加入方单机、守护进程在运行：它丢弃自己的单机圈、换到邀请方的圈（JOIN），不用重启就和新圈的设备连上。
func TestJoinSoloRunningJoinerSwitches(t *testing.T) {
	laptop, tablet, desktop := newDevice(t, "笔记本"), newDevice(t, "平板"), newDevice(t, "台式机")
	laptop.start(t)
	joinCircle(t, laptop, tablet)

	if err := desktop.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	// 单机圈留下的旧密钥：换圈之后不能再留在密钥环里。
	stale, _ := newDiscoveryKey()
	if err := saveRing(desktop.keys, []ringEntry{{Key: stale, Retired: time.Now().Unix()}}); err != nil {
		t.Fatal(err)
	}
	desktop.start(t)
	// 测试里没有 mDNS：平板通过静态地址找到台式机。
	tablet.n.opts.StaticPeers = []string{desktop.n.Addr()}
	tablet.start(t)
	eventually(t, "tablet online", func() bool { return online(t, laptop, tablet.n.id.KeyID) })

	code, _ := invite(t, laptop)
	res, err := desktop.n.Join(t.Context(), code, []string{laptop.n.Addr()}, false)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if res.Mode != pairing.ModeJoin || res.Peer != "笔记本" || len(res.Members) != 3 {
		t.Fatalf("join result = %+v", res)
	}
	if desktop.n.CircleID() != laptop.n.CircleID() {
		t.Fatal("desktop did not switch to the laptop's circle")
	}
	eventually(t, "desktop online with both", func() bool {
		return online(t, desktop, laptop.n.id.KeyID) && online(t, desktop, tablet.n.id.KeyID)
	})
	if ring, err := loadRing(desktop.keys); err != nil || len(ring) != 0 {
		t.Fatalf("key ring after switch = %+v, %v; want empty", ring, err)
	}
}

// 换圈时还在等待的配对码作废：不然守护进程会用它把别的设备拉进新圈。
func TestSwitchCircleVoidsPendingPairing(t *testing.T) {
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	if err := desktop.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	sid, err := desktop.n.Store().CreatePairSession("12345678", pairing.CodeTTL)
	if err != nil {
		t.Fatal(err)
	}
	laptop.start(t)
	joinCircle(t, laptop, desktop)
	ps, _ := desktop.n.Store().GetPairSession(sid)
	if ps.Status != store.PairFailed || ps.Error != store.PairErrCancelled {
		t.Fatalf("pending session after switch = %+v, want cancelled", ps)
	}
}

// 历史按对方的 KeyID 记录，换圈前后都能读出来。
func TestHistoryKeptAcrossSwitch(t *testing.T) {
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	if err := desktop.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	desktop.n.addHistory(store.MessageOut, "OLDPEER", store.MessageDone, HistoryBody{PeerName: "旧设备", Texts: []string{"换圈之前"}})
	laptop.start(t)
	joinCircle(t, laptop, desktop)
	got, err := desktop.n.History("", 10)
	if err != nil || len(got) != 1 || got[0].Body.Texts[0] != "换圈之前" {
		t.Fatalf("history after switch = %+v, %v", got, err)
	}
}

// 两边都有多台设备、邀请方是不支持合并的旧版本（M5a）：什么都不改。
func TestJoinMultiIntoMultiUnsupported(t *testing.T) {
	laptop, tablet := newDevice(t, "笔记本"), newDevice(t, "平板")
	desktop, phone := newDevice(t, "台式机"), newDevice(t, "手机")
	logs := &logRecorder{}
	laptop.n.opts.Logf = logs.Logf
	laptop.n.features = []string{FeatureRename, FeatureLeave, pairing.FeatureCircle2}
	laptop.start(t)
	joinCircle(t, laptop, tablet)
	desktop.start(t)
	joinCircle(t, desktop, phone)
	circleB, circleA := laptop.n.CircleID(), desktop.n.CircleID()

	code, sid := invite(t, laptop)
	_, err := desktop.n.Join(t.Context(), code, []string{laptop.n.Addr()}, true)
	if !errors.Is(err, pairing.ErrMergeUnsupported) {
		t.Fatalf("Join = %v, want ErrMergeUnsupported", err)
	}
	waitRefusalNotCounted(t, laptop, logs, sid, "对方的 Portide 版本不支持合并设备圈")
	// 用户没有同意合并：同样不算输错。
	_, err = desktop.n.Join(t.Context(), code, []string{laptop.n.Addr()}, false)
	if !errors.Is(err, pairing.ErrMergeNotConfirmed) {
		t.Fatalf("Join = %v, want ErrMergeNotConfirmed", err)
	}
	waitRefusalNotCounted(t, laptop, logs, sid, "需要合并两个设备圈")
	if laptop.n.CircleID() != circleB || desktop.n.CircleID() != circleA {
		t.Fatal("a refused merge must not change either circle")
	}
	if laptop.n.isMember(desktop.n.id.KeyID) || desktop.n.isMember(laptop.n.id.KeyID) {
		t.Fatal("a refused merge must not add anyone")
	}
}

// waitRefusalNotCounted 等邀请方记下因为圈子而拒绝（日志含 why），然后确认配对会话没有计入输错、仍然开放。
func waitRefusalNotCounted(t *testing.T, inviter *testDevice, logs *logRecorder, sid int64, why string) {
	t.Helper()
	eventually(t, "inviter logs the refusal", func() bool { return logs.contains(why) })
	ps, _ := inviter.n.Store().GetPairSession(sid)
	if ps.Status != store.PairPending || ps.Attempts != 0 {
		t.Fatalf("pair session after refusal = %+v, want still pending with 0 attempts", ps)
	}
}

// 已经在同一个圈里的设备再配一次：拒绝，但不算输错配对码，配对码还能给别的设备用。
func TestSameCircleRefusalIsNotAWrongCode(t *testing.T) {
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	logs := &logRecorder{}
	laptop.n.opts.Logf = logs.Logf
	laptop.start(t)
	joinCircle(t, laptop, desktop)

	code, sid := invite(t, laptop)
	for i := 0; i < pairing.MaxAttempts; i++ {
		_, err := desktop.n.Join(t.Context(), code, []string{laptop.n.Addr()}, false)
		if !errors.Is(err, pairing.ErrSameCircle) {
			t.Fatalf("attempt %d: Join = %v, want ErrSameCircle", i+1, err)
		}
	}
	waitRefusalNotCounted(t, laptop, logs, sid, "已经在同一个设备圈里")
	if logs.contains("配对失败") {
		t.Fatal("a same-circle refusal must not be logged as a failed attempt")
	}
}

// 设计方案 11：笔记本的圈原来是 {笔记本, 手机, 台式机}，台式机离线期间用户移除了台式机和手机，笔记本单机。
// 台式机拿着旧链输入笔记本的配对码：按 JOIN 重新加入笔记本现在的圈；笔记本不能被旧链覆盖（反向加入），
// 移除记录和换过的发现密钥都保留，手机不会回来。
func TestStaleCopyRejoinKeepsRemovals(t *testing.T) {
	laptop, phone, desktop := newDevice(t, "笔记本"), newDevice(t, "手机"), newDevice(t, "台式机")
	laptop.start(t)
	joinCircle(t, laptop, phone)
	joinCircle(t, laptop, desktop)
	if !desktop.n.isMember(phone.n.id.KeyID) {
		t.Fatal("setup: the desktop's chain must list the phone")
	}
	circle := laptop.n.CircleID()
	for _, d := range []*testDevice{desktop, phone} {
		if err := laptop.n.RemoveDevice(d.n.id.KeyID); err != nil {
			t.Fatal(err)
		}
	}
	keyAfterRemoval := currentKey(laptop)

	code, _ := invite(t, laptop)
	res, err := desktop.n.Join(t.Context(), code, []string{laptop.n.Addr()}, false)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if res.Mode != pairing.ModeJoin {
		t.Fatalf("join result = %+v, want join", res)
	}
	if laptop.n.CircleID() != circle || desktop.n.CircleID() != circle {
		t.Fatal("both devices must stay in the laptop's circle")
	}
	for _, d := range []*testDevice{laptop, desktop} {
		if d.n.isMember(phone.n.id.KeyID) || !d.n.isMember(desktop.n.id.KeyID) {
			t.Fatalf("%s: the phone must stay removed and only the desktop rejoin", d.n.Name())
		}
		if c, _ := d.n.snapshot(); func() bool { _, ok := c.FormerMember(phone.n.id.KeyID); return !ok }() {
			t.Fatalf("%s: the phone's removal record is gone", d.n.Name())
		}
		if !bytes.Equal(currentKey(d), keyAfterRemoval) {
			t.Fatalf("%s: discovery key rolled back to one the phone knows", d.n.Name())
		}
	}
}

// 设计方案 1：台式机输入笔记本的配对码，笔记本提交了、欢迎消息却丢了：台式机已在笔记本的链里，自己还是单机。
// 之后手机加入了台式机的圈。台式机再输入笔记本的配对码：不能按 JOIN 丢下手机，而是两边都有多台设备的拒绝。
func TestLostWelcomeThenNewDeviceNotAbandoned(t *testing.T) {
	laptop, tablet, desktop, phone := newDevice(t, "笔记本"), newDevice(t, "平板"), newDevice(t, "台式机"), newDevice(t, "手机")
	laptop.start(t)
	joinCircle(t, laptop, tablet)
	// 模拟欢迎消息丢失：只在笔记本的链里追加"加入 台式机"。
	c, _ := laptop.n.snapshot()
	next, err := chain.Load(c.Records())
	if err != nil {
		t.Fatal(err)
	}
	if err := next.AddDevice(laptop.n.id, desktop.n.id.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.n.mergeChain(next.Records()); err != nil {
		t.Fatal(err)
	}
	desktop.start(t)
	joinCircle(t, desktop, phone)
	circleA, laptopLen := desktop.n.CircleID(), next.Len()

	code, _ := invite(t, laptop)
	_, err = desktop.n.Join(t.Context(), code, []string{laptop.n.Addr()}, false)
	if !errors.Is(err, pairing.ErrMergeNotConfirmed) {
		t.Fatalf("Join = %v, want ErrMergeNotConfirmed", err)
	}
	if desktop.n.CircleID() != circleA || !desktop.n.isMember(phone.n.id.KeyID) {
		t.Fatal("the desktop must stay in its own circle with the phone")
	}
	if c, _ := laptop.n.snapshot(); c.Len() != laptopLen {
		t.Fatal("the laptop's chain must not change")
	}
}

// 邀请方的链比加入方旧：圈 {笔记本, 手机}，笔记本离线期间手机加了台式机、台式机又移除了手机。
// 台式机输入笔记本的配对码：拒绝为同一个圈；台式机保留自己的链（手机仍被移除、发现密钥不变），
// 笔记本的链里也不会多出"笔记本 加入 台式机"（两条链不分叉）。
func TestStaleInviterCannotRollBackJoiner(t *testing.T) {
	laptop, phone, desktop := newDevice(t, "笔记本"), newDevice(t, "手机"), newDevice(t, "台式机")
	stopLaptop := laptop.startStoppable(t)
	joinCircle(t, laptop, phone)
	stopLaptop()
	laptop.reopen(t)
	stopPhone := phone.startStoppable(t)
	joinCircle(t, phone, desktop)
	stopPhone()
	if err := desktop.n.RemoveDevice(phone.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	desktopChain, _ := desktop.n.snapshot()
	desktopRecords, desktopKey := desktopChain.Records(), currentKey(desktop)
	laptopChain, _ := laptop.n.snapshot()
	laptopLen := laptopChain.Len()

	laptop.start(t)
	code, _ := invite(t, laptop)
	_, err := desktop.n.Join(t.Context(), code, []string{laptop.n.Addr()}, false)
	if !errors.Is(err, pairing.ErrSameCircle) {
		t.Fatalf("Join = %v, want ErrSameCircle", err)
	}
	c, _ := desktop.n.snapshot()
	if !slices.EqualFunc(c.Records(), desktopRecords, bytes.Equal) || desktop.n.isMember(phone.n.id.KeyID) ||
		!bytes.Equal(currentKey(desktop), desktopKey) {
		t.Fatal("the desktop must keep its newer chain and key")
	}
	if c, _ := laptop.n.snapshot(); c.Len() != laptopLen || laptop.n.isMember(desktop.n.id.KeyID) {
		t.Fatal("the laptop must not append a record adding the desktop")
	}
}

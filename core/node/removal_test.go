package node

import (
	"bytes"
	"net"
	"testing"
	"time"

	"portide/core/store"
)

// joinCircle 让 joiner 通过 inviter（守护进程已运行）加入设备圈，不启动 joiner 的守护进程。
func joinCircle(t *testing.T, inviter, joiner *testDevice) {
	t.Helper()
	code, sid := invite(t, inviter)
	if _, err := joiner.n.Join(t.Context(), code, []string{inviter.n.Addr()}, false); err != nil {
		t.Fatalf("Join: %v", err)
	}
	eventually(t, "pair session done", func() bool {
		ps, _ := inviter.n.Store().GetPairSession(sid)
		return ps.Status == store.PairDone
	})
}

func currentKey(d *testDevice) []byte {
	_, key := d.n.snapshot()
	return key
}

// 三台设备：笔记本（邀请方）、台式机、手机。返回时三台都已加入设备圈，只有笔记本在运行。
// logs 不为空时记录笔记本的日志。
func circleOfThree(t *testing.T, logs *logRecorder) (laptop, desktop, phone *testDevice) {
	t.Helper()
	laptop, desktop, phone = newDevice(t, "笔记本"), newDevice(t, "台式机"), newDevice(t, "手机")
	if logs != nil {
		laptop.n.opts.Logf = logs.Logf
	}
	laptop.start(t)
	joinCircle(t, laptop, desktop)
	joinCircle(t, laptop, phone)
	return laptop, desktop, phone
}

// 在另一台成员上（这里用命令行的方式：同一个数据目录另开一个 Node）移除手机：
// 在线的台式机拿到新链和新发现密钥，被移除的手机什么都拿不到。
func TestRemovalDistributesNewKeyToMembersOnly(t *testing.T) {
	laptop, desktop, phone := circleOfThree(t, nil)
	desktop.start(t)
	phone.start(t)
	eventually(t, "all online", func() bool {
		return online(t, laptop, desktop.n.id.KeyID) && online(t, laptop, phone.n.id.KeyID)
	})
	oldKey := currentKey(laptop)

	cli, err := Open(Options{DataDir: laptop.dir, Keys: laptop.keys, DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.RemoveDevice(phone.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	cli.Close()

	eventually(t, "laptop daemon picks up the removal", func() bool { return !laptop.n.isMember(phone.n.id.KeyID) })
	newKey := currentKey(laptop)
	if bytes.Equal(newKey, oldKey) {
		t.Fatal("laptop must have rotated its key")
	}
	eventually(t, "desktop gets the new chain and key", func() bool {
		return !desktop.n.isMember(phone.n.id.KeyID) && bytes.Equal(currentKey(desktop), newKey)
	})
	time.Sleep(300 * time.Millisecond) // 给可能的错误推送留出时间
	if !bytes.Equal(currentKey(phone), oldKey) || !phone.n.isMember(phone.n.id.KeyID) {
		t.Fatal("the removed phone must get neither the new chain nor the new key")
	}
}

// 移除时台式机不在线：它之后用旧密钥连上笔记本（笔记本在宽限期内接受旧密钥），握手后拿到新链和新密钥。
func TestOfflineMemberGetsNewKeyOnReconnect(t *testing.T) {
	laptop, desktop, phone := circleOfThree(t, nil)
	oldKey := currentKey(laptop)

	// 在守护进程自己的 Node 上移除（桌面端界面将这样调用）。
	if err := laptop.n.RemoveDevice(phone.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	newKey := currentKey(laptop)
	if !bytes.Equal(currentKey(desktop), oldKey) {
		t.Fatal("the offline desktop still holds the old key")
	}

	desktop.start(t)
	eventually(t, "desktop reconnects and gets the new key", func() bool {
		return online(t, laptop, desktop.n.id.KeyID) && bytes.Equal(currentKey(desktop), newKey)
	})
	if desktop.n.isMember(phone.n.id.KeyID) {
		t.Fatal("desktop must have merged the removal")
	}
	if got := desktop.n.circleKeys(); len(got) != 2 || !bytes.Equal(got[1], oldKey) {
		t.Fatal("desktop must keep accepting the old key during the grace period")
	}
}

// 被移除的设备立即被断开；它之后用旧密钥重新连接（宽限期内能通过圈子证明），也在出示本机身份之前就被拒绝。
func TestRemovedDeviceIsDisconnectedAndShutOut(t *testing.T) {
	logs := &logRecorder{}
	laptop, desktop, phone := circleOfThree(t, logs)
	desktop.start(t)
	phone.start(t)
	eventually(t, "phone online", func() bool { return connPtr(laptop, phone.n.id.KeyID) != nil })

	if err := laptop.n.RemoveDevice(phone.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "laptop drops the phone", func() bool { return connPtr(laptop, phone.n.id.KeyID) == nil })
	eventually(t, "phone sees laptop offline", func() bool { return !online(t, phone, laptop.n.id.KeyID) })
	if !logs.contains("「手机」已被移出设备圈，已断开连接") {
		t.Fatal("the disconnect must be logged with the removed device's name")
	}
	// 手机每 3 秒重试一次：整段时间里笔记本都不能再登记它，拒绝只记一次日志。
	deadline := time.Now().Add(2*dialInterval + time.Second)
	for time.Now().Before(deadline) {
		if connPtr(laptop, phone.n.id.KeyID) != nil {
			t.Fatal("a removed device must not be able to reconnect")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !logs.contains("已不是本设备圈成员") {
		t.Fatal("the removed device's reconnect attempts must be rejected")
	}
	if n := logs.count("已不是本设备圈成员"); n != 1 {
		t.Fatalf("rejections logged %d times, want once", n)
	}
	if !online(t, laptop, desktop.n.id.KeyID) {
		t.Fatal("the remaining member must stay connected")
	}
}

// 发给被移除设备的排队任务判为失败，历史里记下这次失败（仍显示它的名字）。
func TestQueuedSendToRemovedDeviceFails(t *testing.T) {
	laptop, desktop := pairOffline(t)
	id, err := laptop.n.QueueSend(desktop.n.id.KeyID, plan(t, "来不及送达", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := laptop.n.RemoveDevice(desktop.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendFailed)
	if o, _ := laptop.n.Store().GetOutgoing(id); o.Error != "对方已被移出设备圈" {
		t.Fatalf("task error = %q", o.Error)
	}
	sent, _ := laptop.n.History("", 10)
	if len(sent) != 1 || sent[0].Status != store.MessageFailed || sent[0].Body.PeerName != "台式机" {
		t.Fatalf("history = %+v", sent)
	}
}

// 台式机合并了移除手机的新链、却还没拿到对应的新发现密钥：它仍然用旧密钥主动连接。
// 这时候地址指向的其实是被移除的手机（它也持有这把旧密钥，能通过圈子证明）。
// 台式机的 Hello 必须只出示与旧密钥一致的那段前缀，不能把移除记录透露给手机。
func TestRemovedDeviceGetsNoNewChainFromLaggingDialer(t *testing.T) {
	laptop, desktop, phone := circleOfThree(t, nil)
	phone.start(t)

	if err := laptop.n.RemoveDevice(phone.n.id.KeyID); err != nil {
		t.Fatal(err)
	}

	// 台式机合并了带着移除记录的新链，但密钥环里没有对应的新密钥：继续用旧密钥。
	c, _ := laptop.n.snapshot()
	if _, err := desktop.n.mergeChain(c.Records()); err != nil {
		t.Fatal(err)
	}

	desktop.n.opts.StaticPeers = []string{phone.n.Addr()}
	desktop.start(t)

	time.Sleep(2 * dialInterval)

	if c, _ := phone.n.snapshot(); c.Len() != 3 {
		t.Fatalf("phone 的链长度 = %d，期望仍是 3（不应收到移除记录）", c.Len())
	}
	if !phone.n.isMember(phone.n.id.KeyID) {
		t.Fatal("phone 必须仍然认为自己是成员")
	}
}

func TestRejectionLogIsRateLimited(t *testing.T) {
	logs := &logRecorder{}
	laptop := newDevice(t, "笔记本")
	laptop.n.opts.Logf = logs.Logf
	laptop.start(t)
	a := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 9), Port: 7443}
	b := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 9), Port: 50000} // 同一台主机换了端口
	c := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 10), Port: 7443}
	laptop.n.logRejected(a, "x")
	laptop.n.logRejected(b, "x")
	laptop.n.logRejected(c, "x")
	if n := logs.count("拒绝了来自"); n != 2 {
		t.Fatalf("logged %d rejections, want one per host", n)
	}
}

// 握手刚通过、还没来得及登记连接时，对方就被移出了设备圈：register 必须拒绝登记这条连接，
// 不能让它在 dropRemovedMembers 已经跑过一轮之后又把被移除的设备重新标记为在线。
func TestRegisterRefusesRemovedDevice(t *testing.T) {
	laptop, desktop, phone := circleOfThree(t, nil)
	desktop.start(t)
	phone.start(t)
	eventually(t, "phone online", func() bool { return connPtr(laptop, phone.n.id.KeyID) != nil })
	mc := connPtr(laptop, phone.n.id.KeyID)

	if err := laptop.n.RemoveDevice(phone.n.id.KeyID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "laptop drops the phone", func() bool { return connPtr(laptop, phone.n.id.KeyID) == nil })

	if laptop.n.register(&memberConn{conn: mc.conn, peerID: mc.peerID, dialedBy: mc.dialedBy, tieKey: mc.tieKey}) {
		t.Fatal("register must refuse a device that was removed before registration")
	}
	if connPtr(laptop, phone.n.id.KeyID) != nil {
		t.Fatal("a removed device must not end up registered as connected")
	}
}

package node

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"portide/core/store"
)

func task(t *testing.T, d *testDevice, id int64) *store.Outgoing {
	t.Helper()
	o, err := d.n.Store().GetOutgoing(id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func waitStatus(t *testing.T, d *testDevice, id int64, status string) *store.Outgoing {
	t.Helper()
	var o *store.Outgoing
	eventually(t, "task "+status, func() bool { o = task(t, d, id); return o.Status == status })
	return o
}

func TestFindDevice(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	key := desktop.n.Identity().KeyID
	for _, q := range []string{"台式机", key[:6], key} {
		d, err := laptop.n.FindDevice(q)
		if err != nil || d.KeyID != key {
			t.Errorf("FindDevice(%q) = %+v, %v", q, d, err)
		}
	}
	if _, err := laptop.n.FindDevice("笔记本"); !errors.Is(err, ErrNoSuchDevice) {
		t.Errorf("self must not match: %v", err)
	}
	if _, err := laptop.n.FindDevice("abc"); !errors.Is(err, ErrNoSuchDevice) {
		t.Errorf("short prefix matched: %v", err)
	}
}

func TestQueuedSendIsDelivered(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	p := plan(t, "", map[string]string{"报告.pdf": "pdf-bytes"})
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, p)
	if err != nil {
		t.Fatal(err)
	}
	o := waitStatus(t, laptop, id, store.SendDone)
	if o.BytesDone != p.Total() || o.BytesTotal != p.Total() {
		t.Fatalf("progress %d/%d, want %d", o.BytesDone, o.BytesTotal, p.Total())
	}
	if got := received(t, desktop, "报告.pdf"); got != "pdf-bytes" {
		t.Fatalf("content = %q", got)
	}
}

func TestQueuedSendCanBeCancelledWhileReceiverAsks(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	if err := desktop.n.SetReceivePolicy(ReceivePolicy{Mode: PolicyAsk}); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "", map[string]string{"a.txt": "a"}))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "offer waits in the inbox", func() bool {
		e, _ := desktop.n.Store().PendingInbox(time.Now())
		return len(e) == 1
	})
	if err := laptop.n.Store().CancelOutgoing(id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendCancelled)
	eventually(t, "receiver drops the question", func() bool {
		e, _ := desktop.n.Store().PendingInbox(time.Now())
		return len(e) == 0
	})
}

// 发送途中暂停（接收方在询问时任务处于发送中）：任务停下来、不写历史、对方的询问撤掉；
// 暂停期间对方在线也不发送；继续之后重新报价，接受就送达。
func TestPauseWhileSendingThenResume(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	if err := desktop.n.SetReceivePolicy(ReceivePolicy{Mode: PolicyAsk}); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "", map[string]string{"a.txt": "内容"}))
	if err != nil {
		t.Fatal(err)
	}
	inbox := func() []store.InboxEntry {
		e, _ := desktop.n.Store().PendingInbox(time.Now())
		return e
	}
	eventually(t, "offer waits in the inbox", func() bool { return len(inbox()) == 1 })
	if err := laptop.n.Store().PauseOutgoing(id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "paused task stops sending", func() bool {
		o := task(t, laptop, id)
		return o.Paused && o.Status == store.SendInterrupted
	})
	eventually(t, "receiver drops the question", func() bool { return len(inbox()) == 0 })
	time.Sleep(300 * time.Millisecond) // 几次轮询：暂停的任务不会重新开始
	if o := task(t, laptop, id); o.Status != store.SendInterrupted || o.Attempts != 0 {
		t.Fatalf("paused task restarted or counted as a failure: %+v", o)
	}
	if h, _ := laptop.n.History("", 10); len(h) != 0 {
		t.Fatalf("pausing must not write history: %+v", h)
	}

	if err := laptop.n.Store().ResumeOutgoing(id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "resumed task asks again", func() bool { return len(inbox()) == 1 })
	if err := desktop.n.Store().DecideInbox(inbox()[0].ID, true); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendDone)
}

// pairOffline 让两台设备组成设备圈，但只运行笔记本的守护进程：台式机暂时离线，由测试决定何时 start。
func pairOffline(t *testing.T) (laptop, desktop *testDevice) {
	t.Helper()
	laptop, desktop = newDevice(t, "笔记本"), newDevice(t, "台式机")
	laptop.start(t)
	code, sid := invite(t, laptop)
	if _, err := desktop.n.Join(t.Context(), code, []string{laptop.n.Addr()}, false); err != nil {
		t.Fatalf("Join: %v", err)
	}
	eventually(t, "pair session done", func() bool {
		ps, _ := laptop.n.Store().GetPairSession(sid)
		return ps.Status == store.PairDone
	})
	return laptop, desktop
}

func TestRetryDelay(t *testing.T) {
	for attempts, want := range map[int]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 4: 40 * time.Second, 8: 10 * time.Minute, 50: 10 * time.Minute} {
		if got := retryDelay(attempts); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", attempts, got, want)
		}
	}
}

func TestQueueDays(t *testing.T) {
	d := newDevice(t, "笔记本")
	if got := d.n.QueueDays(); got != DefaultQueueDays {
		t.Fatalf("default = %d", got)
	}
	if err := d.n.SetQueueDays(30); err != nil || d.n.QueueDays() != 30 {
		t.Fatalf("set 30: %v %d", err, d.n.QueueDays())
	}
	for _, bad := range []int{0, -1, 366} {
		if err := d.n.SetQueueDays(bad); err == nil {
			t.Errorf("SetQueueDays(%d) accepted", bad)
		}
	}
}

// 过期未完成的接收不能只在守护进程启动那一刻清理一次：守护进程可能连续运行很多天，
// 过期之后还占着名字直到下次启动是不可接受的（runOutgoing 要按 staleScanEvery 定期清理）。
func TestRunOutgoingPeriodicallyRemovesStaleIncoming(t *testing.T) {
	// staleThreshold 是 max(staleAfter, QueueDays*queueDay)：queueDay 也要缩短，否则默认
	// QueueDays（7）乘真实的 24 小时会让门槛远大于 staleAfter，清理永远不会触发。
	oldAfter, oldScan, oldQueueDay := staleAfter, staleScanEvery, queueDay
	staleAfter, staleScanEvery, queueDay = 50*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { staleAfter, staleScanEvery, queueDay = oldAfter, oldScan, oldQueueDay })

	d := newDevice(t, "笔记本")
	saveDir, err := d.n.SaveDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	k := store.IncomingKey{PeerKey: "PEER", TransferID: "ab"}
	if err := d.n.Store().AddIncoming(k, "d", map[int]string{0: "旧文件.txt"}); err != nil {
		t.Fatal(err)
	}
	// 不断言"启动时清理还没来得及删"：守护进程启动和这条记录的写入时间差是不确定的，断言
	// 容易因为调度抖动而失败。只要证明一直运行下去，不活跃的记录最终会被定期扫描删掉就够了。
	d.start(t)
	eventually(t, "periodic scan removes the now-stale journal", func() bool {
		items, _ := d.n.Store().IncomingItems(k)
		return len(items) == 0
	})
}

// 对方离线时任务一直排队，对方上线后自动发送（设计方案 5.3）。
func TestQueuedSendWaitsForOfflineDevice(t *testing.T) {
	laptop, desktop := pairOffline(t)
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "", map[string]string{"离线.txt": "稍后送达"}))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if o := task(t, laptop, id); o.Status != store.SendPending {
		t.Fatalf("status while offline = %s (%s)", o.Status, o.Error)
	}
	desktop.start(t)
	waitStatus(t, laptop, id, store.SendDone)
	if got := received(t, desktop, "离线.txt"); got != "稍后送达" {
		t.Fatalf("content = %q", got)
	}
}

// 排队超过保留期限仍没有送出：判为失败。
func TestQueuedTaskExpires(t *testing.T) {
	old := queueDay
	queueDay = 50 * time.Millisecond
	t.Cleanup(func() { queueDay = old })
	laptop, desktop := pairOffline(t)
	if err := laptop.n.SetQueueDays(2); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "hi", nil))
	if err != nil {
		t.Fatal(err)
	}
	if o := waitStatus(t, laptop, id, store.SendFailed); o.Error != "对方 2 天内没有上线，已放弃发送" {
		t.Fatalf("error = %q", o.Error)
	}
}

// 任务已经至少重试过一次（对方上线过、但没能在保留期限内送达完成），过期时的措辞要说明
// 这不是"对方一直没上线"，而是"没能送达"。
func TestQueuedTaskExpiresAfterAttempt(t *testing.T) {
	old := queueDay
	queueDay = 50 * time.Millisecond
	t.Cleanup(func() { queueDay = old })
	laptop, desktop := pairOffline(t)
	if err := laptop.n.SetQueueDays(2); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "hi", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := laptop.n.Store().RetryOutgoing(id, 1, time.Time{}, "连接中断"); err != nil {
		t.Fatal(err)
	}
	if o := waitStatus(t, laptop, id, store.SendFailed); o.Error != "2 天内没能送达，已放弃发送" {
		t.Fatalf("error = %q", o.Error)
	}
}

// 排队期间文件被修改：按新内容重新报价（新的传输 ID），对方收到的是新内容。
func TestChangedSourceIsReoffered(t *testing.T) {
	laptop, desktop := pairOffline(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "草稿.txt")
	if err := os.WriteFile(src, []byte("第一版"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := plan(t, "", nil)
	if err := p.AddPath(src); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, p)
	if err != nil {
		t.Fatal(err)
	}
	firstID := task(t, laptop, id).TransferID
	if err := os.WriteFile(src, []byte("第二版，更长一些"), 0o644); err != nil {
		t.Fatal(err)
	}
	desktop.start(t)
	o := waitStatus(t, laptop, id, store.SendDone)
	if bytes.Equal(o.TransferID, firstID) {
		t.Fatal("changed source was sent under the old transfer ID")
	}
	if got := received(t, desktop, "草稿.txt"); got != "第二版，更长一些" {
		t.Fatalf("content = %q", got)
	}
}

// 排队期间文件被删除：任务失败并说明是哪个文件。
func TestMissingSourceFailsTask(t *testing.T) {
	laptop, desktop := pairOffline(t)
	src := filepath.Join(t.TempDir(), "会被删.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := plan(t, "", nil)
	if err := p.AddPath(src); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	desktop.start(t)
	if o := waitStatus(t, laptop, id, store.SendFailed); o.Error != "文件在发送前被移动或删除："+src {
		t.Fatalf("error = %q", o.Error)
	}
}

// 传输途中连接断开：任务转为中断，重新连上后从断点续传，最终文件完整。
func TestQueuedSendResumesAfterReconnect(t *testing.T) {
	logs := &logRecorder{}
	laptop, desktop := newDevice(t, "笔记本"), newDevice(t, "台式机")
	laptop.n.opts.Logf = logs.Logf
	laptop.start(t)
	code, _ := invite(t, laptop)
	if _, err := desktop.n.Join(t.Context(), code, []string{laptop.n.Addr()}, false); err != nil {
		t.Fatal(err)
	}
	desktop.start(t)
	eventually(t, "online", func() bool { return online(t, laptop, desktop.n.Identity().KeyID) })

	data := bytes.Repeat([]byte("portide-resume-"), 6<<20) // 约 78 MiB
	src := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	p := plan(t, "", nil)
	if err := p.AddPath(src); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, p)
	if err != nil {
		t.Fatal(err)
	}
	// 接收方一出现临时文件就切断连接。
	saveDir, _ := desktop.n.SaveDir()
	eventually(t, "receiving started", func() bool {
		entries, _ := os.ReadDir(filepath.Join(saveDir, ".portide-parts"))
		return len(entries) == 1
	})
	mc := connPtr(laptop, desktop.n.Identity().KeyID)
	_ = mc.conn.CloseWithError(0, "network gone")

	eventually(t, "sender notices the interruption", func() bool { return logs.contains("连接中断") })
	waitStatus(t, laptop, id, store.SendDone) // 守护进程每 3 秒重拨，连上后续传
	got, err := os.ReadFile(filepath.Join(saveDir, "big.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("resumed file differs (%d bytes, %v)", len(got), err)
	}
	if !logs.contains("已向「台式机」发送 big.bin") {
		t.Fatal("no completion log")
	}
}

// 轮询读到的快照过时：已经结束的任务不能被再次启动，已完成的任务也不能被改成已取消。
func TestFinishedTaskIsNotRestartedFromStaleSnapshot(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "", map[string]string{"once.txt": "1"}))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendDone)
	stale := *task(t, laptop, id)
	stale.Status = store.SendPending // 模拟轮询时读到的旧状态

	d := laptop.n.daemon
	d.mu.Lock()
	d.sending[id] = true
	mc := d.conns[desktop.n.Identity().KeyID]
	d.mu.Unlock()
	laptop.n.sendTask(t.Context(), stale, mc)
	if o := task(t, laptop, id); o.Status != store.SendDone {
		t.Fatalf("finished task restarted: status %s", o.Status)
	}
	dir, _ := desktop.n.SaveDir()
	if _, err := os.Stat(filepath.Join(dir, "once (1).txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("finished task was sent twice")
	}
}

// 任务排队时就已经带着很长的退避时间（例如命令行手动标记，或测试模拟的旧状态）；对方一旦上线，
// register() 的 RetryNow 必须立刻覆盖这个退避，不用等到 next_at。
func TestReconnectOverridesBackoff(t *testing.T) {
	laptop, desktop := pairOffline(t)
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "", map[string]string{"重连覆盖.txt": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := laptop.n.Store().RetryOutgoing(id, 5, time.Now().Add(time.Hour), "连接中断"); err != nil {
		t.Fatal(err)
	}
	desktop.start(t)
	waitStatus(t, laptop, id, store.SendDone)
}

// 任务的 NextAt 还没到：即使对方在线，守护进程也不能提前开始发送；next_at 一到或被 RetryNow 清零才行。
func TestNextAtIsRespected(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	desktopKey := desktop.n.Identity().KeyID
	id, err := laptop.n.QueueSend(desktopKey, plan(t, "", map[string]string{"按时.txt": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	d := laptop.n.daemon
	d.mu.Lock()
	if d.sending[id] {
		d.mu.Unlock()
		t.Skip("daemon picked the task up first")
	}
	d.sending[id] = true
	d.mu.Unlock()

	if err := laptop.n.Store().RetryOutgoing(id, 1, time.Now().Add(time.Hour), "连接中断"); err != nil {
		t.Fatal(err)
	}

	d.mu.Lock()
	delete(d.sending, id)
	d.mu.Unlock()

	time.Sleep(1500 * time.Millisecond) // 几个轮询周期：确认它一直没被启动
	if o := task(t, laptop, id); o.Status != store.SendInterrupted {
		t.Fatalf("status before next_at = %s", o.Status)
	}

	if err := laptop.n.Store().RetryNow(desktopKey); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendDone)
}

// 对方已经用新连接重新连上、旧连接上的发送才失败：任务必须立即可以重试，不能按退避等待。
func TestFailureAfterReconnectRetriesImmediately(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	desktopKey := desktop.n.Identity().KeyID
	liveConn(t, laptop, desktop) // 当前登记的是一条存活的连接
	// 一条已经关闭的"旧连接"。
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	old, err := laptop.n.daemon.t.DialMember(ctx, desktop.n.Addr())
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	_ = old.CloseWithError(0, "gone")

	id, err := laptop.n.QueueSend(desktopKey, plan(t, "", map[string]string{"重连.txt": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	d := laptop.n.daemon
	d.mu.Lock()
	if d.sending[id] {
		d.mu.Unlock()
		t.Skip("daemon picked the task up first")
	}
	d.sending[id] = true
	d.mu.Unlock()
	laptop.n.sendTask(t.Context(), *task(t, laptop, id), &memberConn{conn: old, peerID: desktopKey})
	o := task(t, laptop, id)
	if o.Status != store.SendInterrupted || o.Attempts != 1 || !o.NextAt.IsZero() {
		t.Fatalf("after failure on the old link: status %s attempts %d next %v", o.Status, o.Attempts, o.NextAt)
	}
	waitStatus(t, laptop, id, store.SendDone)
}

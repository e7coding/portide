package app

import (
	"sync"
	"testing"
	"time"

	"portide/core/store"
	"portide/core/transfer"
)

func TestSpeedMeter(t *testing.T) {
	var m speedMeter
	t0 := time.Unix(1000, 0)
	at := func(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

	it := TransferItem{Key: "o1", BytesDone: 0, BytesTotal: 10000}
	m.fill(&it, at(0))
	if it.BytesPerSec != 0 || it.EtaSec != -1 {
		t.Fatalf("first sample: %+v", it)
	}
	it.BytesDone = 1000
	m.fill(&it, at(1))
	if it.BytesPerSec != 1000 || it.EtaSec != 9 {
		t.Fatalf("after 1s: %+v", it)
	}
	// 平均最近 5 秒：前 5 秒每秒 1000，之后每秒 3000。
	for sec := 2; sec <= 5; sec++ {
		it.BytesDone = int64(sec) * 1000
		m.fill(&it, at(float64(sec)))
	}
	for sec := 6; sec <= 10; sec++ {
		it.BytesDone = 5000 + int64(sec-5)*3000
		m.fill(&it, at(float64(sec)))
	}
	if it.BytesPerSec != 3000 {
		t.Fatalf("window average = %d, want 3000", it.BytesPerSec)
	}
	// 字节数变小（续传从头开始）：重新计算，不出现负数。
	it.BytesDone = 100
	m.fill(&it, at(11))
	if it.BytesPerSec != 0 || it.EtaSec != -1 {
		t.Fatalf("after a reset: %+v", it)
	}
	// 暂停很久之后继续：不和暂停前的样本平均。
	it.BytesDone = 200
	m.fill(&it, at(40))
	if it.BytesPerSec != 0 {
		t.Fatalf("after a long gap: %+v", it)
	}
	it.BytesDone = 2200
	m.fill(&it, at(41))
	if it.BytesPerSec != 2000 {
		t.Fatalf("speed after resuming = %d, want 2000", it.BytesPerSec)
	}
	// 剩余时间向上取整。
	other := TransferItem{Key: "i2", BytesTotal: 2500}
	m.fill(&other, at(11))
	other.BytesDone = 1000
	m.fill(&other, at(12))
	if other.EtaSec != 2 {
		t.Fatalf("eta = %d, want 2", other.EtaSec)
	}
	m.prune(at(41).Add(speedStale + time.Second))
	if len(m.samples) != 0 {
		t.Fatalf("stale entries kept: %v", m.samples)
	}
}

// 对方离线时的排队任务：可以暂停、继续；结束之后再暂停会报错。传输页和时间线都显示暂停状态。
func TestPauseAndResumeSend(t *testing.T) {
	laptop, desktop := pairedServices(t)
	target := keyOf(t, desktop)
	Stop(desktop)
	eventually(t, "desktop offline", func() bool { return !online(t, laptop, "台式机") })

	if _, err := laptop.SendText([]string{target}, "稍后送达"); err != nil {
		t.Fatal(err)
	}
	list, err := laptop.Transfers()
	if err != nil || len(list.Outgoing) != 1 || len(list.Incoming) != 0 || list.Count() != 1 {
		t.Fatalf("Transfers = %+v, %v", list, err)
	}
	task := list.Outgoing[0]
	if task.Paused || task.PeerName != "台式机" || task.Summary == "" || task.Status != store.SendPending || task.EtaSec != -1 || task.Key == "" {
		t.Fatalf("task = %+v", task)
	}
	if err := laptop.PauseSend(task.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = laptop.Transfers()
	if !list.Outgoing[0].Paused || !timeline(t, laptop)[0].Paused {
		t.Fatalf("not paused: %+v / %+v", list.Outgoing[0], timeline(t, laptop)[0])
	}
	if err := laptop.ResumeSend(task.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = laptop.Transfers(); list.Outgoing[0].Paused {
		t.Fatalf("still paused: %+v", list.Outgoing[0])
	}
	if err := laptop.CancelSend(task.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "task finished", func() bool { list, _ := laptop.Transfers(); return list.Count() == 0 })
	if err := laptop.PauseSend(task.ID); err == nil || err.Error() != "这个任务已经结束" {
		t.Fatalf("pausing a finished task: %v", err)
	}
	if err := laptop.ResumeSend(task.ID); err == nil || err.Error() != "这个任务已经结束" {
		t.Fatalf("resuming a finished task: %v", err)
	}
}

func TestStopReceiveUnknown(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "笔记本")
	if err := s.StopReceive("FFFF", "00"); err == nil || err.Error() != "这次接收已经结束" {
		t.Fatalf("StopReceive: %v", err)
	}
	list, err := s.Transfers()
	if err != nil || list.Outgoing == nil || list.Incoming == nil || list.Count() != 0 {
		t.Fatalf("Transfers = %+v, %v", list, err)
	}
}

// 全部暂停 / 全部继续：对方离线时排队的任务一起暂停、一起继续；排队和暂停的都不算"正在传输"。
func TestPauseAllAndResumeAll(t *testing.T) {
	laptop, desktop := pairedServices(t)
	target := keyOf(t, desktop)
	Stop(desktop)
	eventually(t, "desktop offline", func() bool { return !online(t, laptop, "台式机") })
	for _, text := range []string{"一", "二"} {
		if _, err := laptop.SendText([]string{target}, text); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := laptop.PauseAll(); err != nil || n != 2 {
		t.Fatalf("PauseAll = %d %v, want 2", n, err)
	}
	if n, _ := laptop.PauseAll(); n != 0 {
		t.Fatalf("pausing again paused %d, want 0", n)
	}
	list, err := laptop.Transfers()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Outgoing) != 2 || !list.Outgoing[0].Paused || !list.Outgoing[1].Paused || list.Active != 0 || list.Count() != 2 {
		t.Fatalf("transfers after PauseAll = %+v", list)
	}
	if n, err := laptop.ResumeAll(); err != nil || n != 2 {
		t.Fatalf("ResumeAll = %d %v, want 2", n, err)
	}
	if list, _ := laptop.Transfers(); list.Outgoing[0].Paused || list.Outgoing[1].Paused || list.Active != 0 {
		t.Fatalf("transfers after ResumeAll = %+v", list)
	}
}

// interruptedTask 建一个发给 target 的文本任务，并把它标成"上次发送失败、一小时后才自动重试"
// （和 sendTask 在连接出错时写的一样）。守护进程抢先开始发送时跳过测试。
func interruptedTask(t *testing.T, s *Service, target, text string) int64 {
	t.Helper()
	n, err := s.node()
	if err != nil {
		t.Fatal(err)
	}
	var p transfer.Plan
	if err := p.AddText(text, time.Now()); err != nil {
		t.Fatal(err)
	}
	id, err := n.QueueSend(target, &p)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Store().RetryOutgoing(id, 3, time.Now().Add(time.Hour), "连接中断"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // 几个轮询周期：确认守护进程没有提前开始
	o, err := n.Store().GetOutgoing(id)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != store.SendInterrupted || o.Attempts != 3 {
		t.Skipf("daemon picked the task up first (status %s)", o.Status)
	}
	return id
}

// received 返回 s 收到的、内容为 text 的消息条数。
func received(t *testing.T, s *Service, text string) int {
	t.Helper()
	count := 0
	for _, it := range timeline(t, s) {
		if it.Kind == KindMessage && it.Direction == store.MessageIn && len(it.Texts) == 1 && it.Texts[0] == text {
			count++
		}
	}
	return count
}

// 发送失败后等自动重试：手动重试立即发出，不用等退避时间；结束之后不能再重试。
func TestRetrySendAfterFailure(t *testing.T) {
	laptop, desktop := pairedServices(t)
	id := interruptedTask(t, laptop, keyOf(t, desktop), "手动重试")

	list, err := laptop.Transfers()
	if err != nil || len(list.Outgoing) != 1 || list.Outgoing[0].Status != store.SendInterrupted || list.Outgoing[0].Error != "连接中断" {
		t.Fatalf("Transfers before retry = %+v, %v", list, err)
	}
	if err := laptop.RetrySend(id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "task finished", func() bool { list, _ := laptop.Transfers(); return list.Count() == 0 })
	eventually(t, "message received", func() bool { return received(t, desktop, "手动重试") == 1 })
	if items := timeline(t, laptop); len(items) != 1 || items[0].Kind != KindMessage || items[0].Status != store.MessageDone {
		t.Fatalf("laptop timeline = %+v", items)
	}
	if err := laptop.RetrySend(id); err == nil || err.Error() != "这个任务已经结束" {
		t.Fatalf("retrying a finished task: %v", err)
	}
}

// 手动重试和自动重试（对方重新上线时的立即重试）同时发生，手动重试又被连点：只送达一次，任务以已发送结束。
func TestRetrySendRacesAutomaticRetry(t *testing.T) {
	laptop, desktop := pairedServices(t)
	target := keyOf(t, desktop)
	id := interruptedTask(t, laptop, target, "只送一次")
	n, _ := laptop.node()

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 64)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// 一直连点到任务结束：中间会遇到"正在发送"，不能因此再发一次。
			for {
				err := laptop.RetrySend(id)
				if err != nil && err.Error() == "这个任务已经结束" {
					return
				}
				if err != nil && err.Error() != "这个任务正在发送" {
					errs <- err
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if err := n.Store().RetryNow(target); err != nil {
			errs <- err
		}
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("RetrySend: %v", err)
	}

	o, err := n.Store().GetOutgoing(id)
	if err != nil || o.Status != store.SendDone {
		t.Fatalf("task = %+v, %v", o, err)
	}
	eventually(t, "message received", func() bool { return received(t, desktop, "只送一次") >= 1 })
	time.Sleep(300 * time.Millisecond) // 多等几个轮询周期：确认没有第二份
	if got := received(t, desktop, "只送一次"); got != 1 {
		t.Fatalf("received %d copies, want 1", got)
	}
	if items := timeline(t, laptop); len(items) != 1 || items[0].Status != store.MessageDone {
		t.Fatalf("laptop timeline = %+v", items)
	}
}

// 不能手动重试的任务：已暂停（要点继续）、对方不在线（上线后自动发送）。
func TestRetrySendRefusals(t *testing.T) {
	laptop, desktop := pairedServices(t)
	target := keyOf(t, desktop)
	Stop(desktop)
	eventually(t, "desktop offline", func() bool { return !online(t, laptop, "台式机") })
	if _, err := laptop.SendText([]string{target}, "离线"); err != nil {
		t.Fatal(err)
	}
	list, _ := laptop.Transfers()
	id := list.Outgoing[0].ID
	if err := laptop.RetrySend(id); err == nil || err.Error() != "「台式机」现在不在线，上线后会自动发送" {
		t.Fatalf("retrying while offline: %v", err)
	}
	if err := laptop.PauseSend(id); err != nil {
		t.Fatal(err)
	}
	if err := laptop.RetrySend(id); err == nil || err.Error() != "这个任务已暂停，请点继续" {
		t.Fatalf("retrying a paused task: %v", err)
	}
	if err := laptop.RetrySend(12345); err == nil || err.Error() != "这个任务已经结束" {
		t.Fatalf("retrying an unknown task: %v", err)
	}
}

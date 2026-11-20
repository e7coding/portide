package store

import (
	"errors"
	"testing"
	"time"
)

func TestOutgoingLifecycle(t *testing.T) {
	s := open(t)
	id, err := s.CreateOutgoing("PEER", []byte{1, 2}, []byte("offer"), []byte("[]"), 100)
	if err != nil {
		t.Fatal(err)
	}
	open1, _ := s.OpenOutgoing()
	if len(open1) != 1 || open1[0].Status != SendPending || open1[0].BytesTotal != 100 || string(open1[0].Offer) != "offer" {
		t.Fatalf("OpenOutgoing = %+v", open1)
	}
	_ = s.UpdateOutgoing(id, SendActive, "")
	_ = s.SetOutgoingProgress(id, 40)
	_ = s.CancelOutgoing(id)
	o, err := s.GetOutgoing(id)
	if err != nil || o.Status != SendActive || o.BytesDone != 40 || !o.Cancel {
		t.Fatalf("GetOutgoing = %+v, %v", o, err)
	}
	// 守护进程重启：进行中的任务改为中断。
	_ = s.RecoverOutgoing()
	if o, _ := s.GetOutgoing(id); o.Status != SendInterrupted {
		t.Fatalf("after recover: %s", o.Status)
	}
	_ = s.UpdateOutgoing(id, SendDone, "")
	if open2, _ := s.OpenOutgoing(); len(open2) != 0 {
		t.Fatalf("finished task still open: %+v", open2)
	}
	_ = s.PruneOutgoing(time.Now().Add(-time.Hour))
	if _, err := s.GetOutgoing(id); err != nil {
		t.Fatal("recent task pruned")
	}
	_ = s.PruneOutgoing(time.Now().Add(time.Hour))
	if _, err := s.GetOutgoing(id); err == nil {
		t.Fatal("old finished task not pruned")
	}
}

func TestIncomingJournal(t *testing.T) {
	s := open(t)
	k := IncomingKey{PeerKey: "PEER", TransferID: "abcd"}
	if items, err := s.IncomingItems(k); err != nil || len(items) != 0 {
		t.Fatalf("empty journal: %v %v", items, err)
	}
	if err := s.AddIncoming(k, "d1", map[int]string{0: "照片 (1)", 1: "照片 (1)/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	_ = s.MarkIncomingDone(k, 1)
	items, _ := s.IncomingItems(k)
	if items[0].Path != "照片 (1)" || items[0].Done || items[1].Path != "照片 (1)/a.jpg" || !items[1].Done || items[0].Digest != "d1" {
		t.Fatalf("items = %+v", items)
	}
	if stale, _ := s.StaleIncoming(time.Now().Add(-time.Hour)); len(stale) != 0 {
		t.Fatalf("fresh transfer reported stale: %v", stale)
	}
	if stale, _ := s.StaleIncoming(time.Now().Add(time.Hour)); len(stale) != 1 || stale[0] != k {
		t.Fatalf("StaleIncoming = %v", stale)
	}
	_ = s.DeleteIncoming(k)
	if items, _ := s.IncomingItems(k); len(items) != 0 {
		t.Fatalf("journal not deleted: %v", items)
	}
}

func TestInbox(t *testing.T) {
	s := open(t)
	id, err := s.CreateInbox("手机", "a.jpg", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expired, _ := s.CreateInbox("手机", "b.jpg", 10, -time.Second)
	pend, _ := s.PendingInbox(time.Now())
	if len(pend) != 1 || pend[0].ID != id || pend[0].PeerName != "手机" {
		t.Fatalf("PendingInbox = %+v", pend)
	}
	if err := s.DecideInbox(expired, true); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decide expired: %v", err)
	}
	if err := s.DecideInbox(id, true); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideInbox(id, false); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decide twice: %v", err)
	}
	if e, _ := s.GetInbox(id); e.Status != InboxAccepted {
		t.Fatalf("status = %s", e.Status)
	}
	_ = s.DeleteInbox(id)
	if _, err := s.GetInbox(id); err == nil {
		t.Fatal("inbox entry not deleted")
	}
}

func TestClearInbox(t *testing.T) {
	s := open(t)
	id1, err := s.CreateInbox("手机", "a.jpg", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.CreateInbox("平板", "b.jpg", 20, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ClearInbox(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInbox(id1); err == nil {
		t.Fatal("entry 1 not cleared")
	}
	if _, err := s.GetInbox(id2); err == nil {
		t.Fatal("entry 2 not cleared")
	}
	if pend, _ := s.PendingInbox(time.Now()); len(pend) != 0 {
		t.Fatalf("PendingInbox after clear = %+v", pend)
	}
}

func TestOutgoingQueue(t *testing.T) {
	s := open(t)
	before := time.Now().Add(-time.Second)
	id, err := s.CreateOutgoing("PEER", []byte{1}, []byte("offer"), []byte("[]"), 10)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := s.GetOutgoing(id)
	if o.CreatedAt.Before(before) || o.Attempts != 0 || !o.NextAt.IsZero() {
		t.Fatalf("new task = %+v", o)
	}
	next := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	if err := s.RetryOutgoing(id, 2, next, "网络断了"); err != nil {
		t.Fatal(err)
	}
	o, _ = s.GetOutgoing(id)
	if o.Status != SendInterrupted || o.Attempts != 2 || !o.NextAt.Equal(next) || o.Error != "网络断了" {
		t.Fatalf("after retry = %+v", o)
	}
	_ = s.RetryNow("OTHER")
	if o, _ = s.GetOutgoing(id); o.NextAt.IsZero() {
		t.Fatal("RetryNow touched another peer's task")
	}
	_ = s.RetryNow("PEER")
	if o, _ = s.GetOutgoing(id); !o.NextAt.IsZero() {
		t.Fatal("RetryNow did not reset the task")
	}
	_ = s.RetryOutgoing(id, 3, time.Time{}, "守护进程已退出")
	if o, _ = s.GetOutgoing(id); !o.NextAt.IsZero() || o.Attempts != 3 {
		t.Fatalf("zero next time not stored as zero: %+v", o)
	}
	if err := s.ReplaceOffer(id, []byte{2}, []byte("offer2"), 20); err != nil {
		t.Fatal(err)
	}
	o, _ = s.GetOutgoing(id)
	if string(o.TransferID) != "\x02" || string(o.Offer) != "offer2" || o.BytesTotal != 20 || o.BytesDone != 0 {
		t.Fatalf("after replace = %+v", o)
	}
	_ = s.UpdateOutgoing(id, SendDone, "")
	if all, _ := s.ListOutgoing(); len(all) != 1 || all[0].Status != SendDone {
		t.Fatalf("ListOutgoing = %+v", all)
	}
	_ = s.RetryNow("PEER") // 已结束的任务不受影响
	if o, _ = s.GetOutgoing(id); o.Status != SendDone {
		t.Fatalf("finished task changed: %s", o.Status)
	}
}

// created_at 为 0 说明这一行是旧版本写入的（没有这一列时的默认值）：退回用 updated_at 当创建时间，
// 不要把任务的排队有效期算成"从 1970 年开始"。
func TestScanOutgoingFallsBackToUpdatedAtWhenCreatedAtIsZero(t *testing.T) {
	s := open(t)
	res, err := s.db.Exec(`INSERT INTO outgoing (peer_key, transfer_id, offer, sources, status, updated_at, created_at) VALUES (?, ?, ?, ?, ?, ?, 0)`,
		"PEER", []byte{9}, []byte("offer"), []byte("[]"), SendPending, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	o, err := s.GetOutgoing(id)
	if err != nil {
		t.Fatal(err)
	}
	if o.CreatedAt.IsZero() || !o.CreatedAt.Equal(o.UpdatedAt) {
		t.Fatalf("CreatedAt = %v, UpdatedAt = %v", o.CreatedAt, o.UpdatedAt)
	}
}

func TestIncomingKeys(t *testing.T) {
	s := open(t)
	_ = s.AddIncoming(IncomingKey{PeerKey: "A", TransferID: "1"}, "d", map[int]string{0: "照片"})
	_ = s.AddIncoming(IncomingKey{PeerKey: "A", TransferID: "2"}, "d", map[int]string{0: "b.txt"})
	_ = s.AddIncoming(IncomingKey{PeerKey: "B", TransferID: "3"}, "d", map[int]string{0: "c.txt"})
	keys, err := s.IncomingKeys("A")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, k := range keys {
		if k.PeerKey != "A" {
			t.Fatalf("IncomingKeys(\"A\") returned peer %q", k.PeerKey)
		}
		got[k.TransferID] = true
	}
	if len(got) != 2 || !got["1"] || !got["2"] {
		t.Fatalf("IncomingKeys(\"A\") = %v", keys)
	}
	if keys, _ := s.IncomingKeys("nobody"); len(keys) != 0 {
		t.Fatalf("IncomingKeys for unknown peer = %v", keys)
	}
}

func TestIncomingTops(t *testing.T) {
	s := open(t)
	_ = s.AddIncoming(IncomingKey{PeerKey: "A", TransferID: "1"}, "d", map[int]string{0: "照片", 1: "照片/1.jpg", 2: ""})
	_ = s.AddIncoming(IncomingKey{PeerKey: "B", TransferID: "2"}, "d", map[int]string{0: "a.txt"})
	tops, err := s.IncomingTops()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range tops {
		got[n] = true
	}
	if len(got) != 2 || !got["照片"] || !got["a.txt"] {
		t.Fatalf("IncomingTops = %v", tops)
	}
}

// 暂停和继续只对还没结束的任务生效；继续之后排队期限从继续的时间算起。
func TestPauseAndResumeOutgoing(t *testing.T) {
	s := open(t)
	id, err := s.CreateOutgoing("PEER", []byte{1}, []byte("offer"), []byte("[]"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PauseOutgoing(id); err != nil {
		t.Fatal(err)
	}
	o, err := s.GetOutgoing(id)
	if err != nil || !o.Paused || !o.ResumedAt.IsZero() || o.QueuedSince() != o.CreatedAt {
		t.Fatalf("after pause: %+v, %v", o, err)
	}
	if open, _ := s.OpenOutgoing(); len(open) != 1 {
		t.Fatal("a paused task is still open (the daemon decides to skip it)")
	}
	if err := s.RetryOutgoing(id, 0, time.Now().Add(time.Hour), "连接中断"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeOutgoing(id); err != nil {
		t.Fatal(err)
	}
	o, _ = s.GetOutgoing(id)
	if o.Paused || o.ResumedAt.IsZero() || !o.NextAt.IsZero() || o.QueuedSince() != o.ResumedAt {
		t.Fatalf("after resume: %+v", o)
	}
	if err := s.UpdateOutgoing(id, SendDone, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PauseOutgoing(id); err != ErrTaskFinished {
		t.Fatalf("pausing a finished task: err = %v", err)
	}
	if err := s.ResumeOutgoing(9999); err != ErrTaskFinished {
		t.Fatalf("resuming a missing task: err = %v", err)
	}
}

// 手动重试只清零中断（或排队）任务的 next_at，不动失败次数；正在发送、暂停、取消、结束的任务不受影响。
func TestRetryOutgoingNow(t *testing.T) {
	s := open(t)
	id, err := s.CreateOutgoing("PEER", []byte{1}, []byte("offer"), []byte("[]"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RetryOutgoing(id, 3, time.Now().Add(time.Hour), "连接中断"); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryOutgoingNow(id); err != nil {
		t.Fatal(err)
	}
	o, _ := s.GetOutgoing(id)
	if o.Status != SendInterrupted || o.Attempts != 3 || !o.NextAt.IsZero() || o.Error != "连接中断" {
		t.Fatalf("after retry now: %+v", o)
	}

	if err := s.UpdateOutgoing(id, SendActive, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryOutgoingNow(id); err != ErrTaskFinished {
		t.Fatalf("retrying a task that is sending: err = %v", err)
	}
	if err := s.RetryOutgoing(id, 4, time.Now().Add(time.Hour), "连接中断"); err != nil {
		t.Fatal(err)
	}
	if err := s.PauseOutgoing(id); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryOutgoingNow(id); err != ErrTaskFinished {
		t.Fatalf("retrying a paused task: err = %v", err)
	}
	if o, _ := s.GetOutgoing(id); o.NextAt.IsZero() {
		t.Fatal("retrying a paused task cleared next_at")
	}
	if err := s.ResumeOutgoing(id); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelOutgoing(id); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryOutgoingNow(id); err != ErrTaskFinished {
		t.Fatalf("retrying a cancelled task: err = %v", err)
	}
	if err := s.UpdateOutgoing(id, SendFailed, "对方拒绝了"); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryOutgoingNow(id); err != ErrTaskFinished {
		t.Fatalf("retrying a failed task: err = %v", err)
	}
}

func TestStoppedIncoming(t *testing.T) {
	s := open(t)
	k := IncomingKey{PeerKey: "PEER", TransferID: "0102"}
	if stopped, err := s.IsStoppedIncoming(k); err != nil || stopped {
		t.Fatalf("fresh: %v, %v", stopped, err)
	}
	if err := s.AddStoppedIncoming(k); err != nil {
		t.Fatal(err)
	}
	if err := s.AddStoppedIncoming(k); err != nil { // 重复停止不报错
		t.Fatal(err)
	}
	if stopped, _ := s.IsStoppedIncoming(k); !stopped {
		t.Fatal("stop not recorded")
	}
	if stopped, _ := s.IsStoppedIncoming(IncomingKey{PeerKey: "OTHER", TransferID: "0102"}); stopped {
		t.Fatal("a different peer must not match")
	}
	if err := s.PruneStoppedIncoming(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if stopped, _ := s.IsStoppedIncoming(k); stopped {
		t.Fatal("old stop records must be pruned")
	}
}

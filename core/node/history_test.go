package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"portide/core/pb"
	"portide/core/store"
	"portide/core/transfer"
)

// 发出和收到的内容各自记进本机的历史：文本全文、文件清单、保存位置。
func TestHistoryRecordsSendAndReceive(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	src := filepath.Join(t.TempDir(), "周报.docx")
	if err := os.WriteFile(src, []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := plan(t, "今晚八点开会", nil)
	if err := p.AddPath(src); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, p)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendDone)

	sent, err := laptop.n.History("", 10)
	if err != nil || len(sent) != 1 {
		t.Fatalf("sender history = %+v, %v", sent, err)
	}
	if e := sent[0]; e.Direction != store.MessageOut || e.Status != store.MessageDone || e.PeerKey != desktop.n.Identity().KeyID ||
		e.Files != 1 || e.Texts != 1 || e.Total != 4 || e.Body.PeerName != "台式机" || len(e.Body.Texts) != 1 || e.Body.Texts[0] != "今晚八点开会" ||
		len(e.Body.Files) != 1 || e.Body.Files[0] != (HistoryFile{Path: "周报.docx", Size: 4, Local: src}) {
		t.Fatalf("sender entry = %+v", e)
	}

	var got []HistoryEntry
	eventually(t, "receiver history", func() bool {
		got, _ = desktop.n.History("", 10)
		return len(got) == 1
	})
	dir, _ := desktop.n.SaveDir()
	if e := got[0]; e.Direction != store.MessageIn || e.Status != store.MessageDone || e.PeerKey != laptop.n.Identity().KeyID ||
		e.Body.PeerName != "笔记本" || e.Body.Dir != dir || len(e.Body.Files) != 1 || e.Body.Files[0] != (HistoryFile{Path: "周报.docx", Size: 4}) ||
		len(e.Body.Texts) != 1 || e.Body.Texts[0] != "今晚八点开会" {
		t.Fatalf("receiver entry = %+v", e)
	}
	one, err := desktop.n.HistoryEntry(got[0].ID)
	if err != nil || one.Body.Texts[0] != "今晚八点开会" {
		t.Fatalf("HistoryEntry = %+v, %v", one, err)
	}
	if _, err := desktop.n.HistoryEntry(got[0].ID + 100); !errors.Is(err, store.ErrNoMessage) {
		t.Fatalf("missing entry: %v", err)
	}
	if fromOther, _ := desktop.n.History("FFFF", 10); len(fromOther) != 0 {
		t.Fatalf("device filter = %+v", fromOther)
	}
}

// 被拒绝的发送只在发送方留下一条"失败"，原因是给用户看的文字。
func TestHistoryRecordsRejectedSend(t *testing.T) {
	laptop, desktop := pairOnline(t, nil)
	if err := desktop.n.SetReceivePolicy(ReceivePolicy{Mode: PolicyAsk}); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "", map[string]string{"不要.txt": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	var pending []store.InboxEntry
	eventually(t, "offer waits in the inbox", func() bool {
		pending, _ = desktop.n.Store().PendingInbox(time.Now())
		return len(pending) == 1
	})
	if err := desktop.n.Store().DecideInbox(pending[0].ID, false); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendFailed)
	sent, _ := laptop.n.History("", 10)
	if len(sent) != 1 || sent[0].Status != store.MessageFailed || sent[0].Body.Error != "对方拒绝了" {
		t.Fatalf("sender history = %+v", sent)
	}
	time.Sleep(200 * time.Millisecond)
	if got, _ := desktop.n.History("", 10); len(got) != 0 {
		t.Fatalf("receiver recorded a rejected offer: %+v", got)
	}
}

// 中断的接收不记，等它最终结束时才记（Result 里带着续传前收齐的文件）。
func TestRecordReceiveSkipsInterruptions(t *testing.T) {
	d := newDevice(t, "台式机")
	o := &pb.Offer{TransferId: transfer.NewTransferID(), Items: []*pb.Item{{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "a.txt", Size: 1}}}
	d.n.recordReceive("PEER", o, transfer.Result{End: transfer.EndInterrupted})
	if got, _ := d.n.History("", 10); len(got) != 0 {
		t.Fatalf("interruption recorded: %+v", got)
	}
	d.n.recordReceive("PEER", o, transfer.Result{End: transfer.EndPartial, SaveDir: "/s", Saved: []transfer.SavedFile{{Path: "a.txt", Size: 1}}})
	got, _ := d.n.History("", 10)
	if len(got) != 1 || got[0].Status != store.MessagePartial || got[0].Body.Dir != "/s" || got[0].Files != 1 {
		t.Fatalf("partial = %+v", got)
	}
}

// 停止接收时一个文件都还没收到：记录里仍要有报价摘要，时间线才能显示收的是什么。
func TestStoppedReceiveKeepsSummary(t *testing.T) {
	d := newDevice(t, "台式机")
	o := &pb.Offer{TransferId: transfer.NewTransferID(), Items: []*pb.Item{{Kind: pb.ItemKind_ITEM_KIND_FILE, Path: "季度报告.pdf", Size: 9}}}
	d.n.recordReceive("PEER", o, transfer.Result{End: transfer.EndStopped, SaveDir: "/s"})
	d.n.recordReceive("PEER", o, transfer.Result{End: transfer.EndComplete, SaveDir: "/s", Saved: []transfer.SavedFile{{Path: "季度报告.pdf", Size: 9}}})
	got, _ := d.n.History("", 10)
	if len(got) != 2 {
		t.Fatalf("history = %+v", got)
	}
	if got[0].Body.Summary != "" {
		t.Fatalf("a complete receive needs no summary, got %q", got[0].Body.Summary)
	}
	if b := got[1].Body; b.Summary != "季度报告.pdf（9 B）" || b.Error != "已停止接收" || len(b.Files) != 0 {
		t.Fatalf("stopped receive body = %+v", b)
	}
}

func TestHistoryDays(t *testing.T) {
	d := newDevice(t, "笔记本")
	if d.n.HistoryDays() != 0 {
		t.Fatalf("default = %d", d.n.HistoryDays())
	}
	for _, bad := range []int{-1, 3651} {
		if err := d.n.SetHistoryDays(bad); err == nil {
			t.Errorf("SetHistoryDays(%d) accepted", bad)
		}
	}
	if _, err := d.n.Store().AddMessage(store.Message{Direction: store.MessageIn, PeerKey: "P", Status: store.MessageDone, Body: []byte(`{"peer":"旧"}`), CreatedAt: time.Now().Add(-72 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.n.Store().AddMessage(store.Message{Direction: store.MessageIn, PeerKey: "P", Status: store.MessageDone, Body: []byte(`{"peer":"新"}`)}); err != nil {
		t.Fatal(err)
	}
	d.n.pruneHistory(time.Now()) // 0 天：永久保留
	if got, _ := d.n.History("", 10); len(got) != 2 {
		t.Fatalf("pruned with history-days 0: %d left", len(got))
	}
	if err := d.n.SetHistoryDays(1); err != nil {
		t.Fatal(err)
	}
	d.n.pruneHistory(time.Now())
	if got, _ := d.n.History("", 10); len(got) != 1 || got[0].Body.PeerName != "新" {
		t.Fatalf("after prune = %+v", got)
	}
}

// 纯文本的发送：文本内容只进历史，接收方日志里一个字都不能有（含"开始接收""已收到"这些带摘要的行）。
func TestLogsNeverContainText(t *testing.T) {
	logs := &logRecorder{}
	laptop, desktop := pairOnline(t, logs)
	if _, err := send(t, liveConn(t, laptop, desktop), plan(t, "银行卡尾号 1234", nil)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "receiver history", func() bool {
		got, _ := desktop.n.History("", 10)
		return len(got) == 1 && len(got[0].Body.Texts) == 1 && got[0].Body.Texts[0] == "银行卡尾号 1234"
	})
	eventually(t, "receive logged with summary not content", func() bool {
		return logs.contains("已收到「笔记本」发来的 1 段文本")
	})
	if logs.contains("银行卡") {
		t.Fatal("text content leaked into the log")
	}
}

// 队列中的任务被取消时，发送方的历史记录这次取消。
func TestHistoryRecordsQueuedCancel(t *testing.T) {
	laptop, desktop := pairOffline(t) // 台式机不上线，任务一直排队
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "要取消的", nil))
	if err != nil {
		t.Fatal(err)
	}

	if err := laptop.n.Store().CancelOutgoing(id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendCancelled)

	sent, _ := laptop.n.History("", 10)
	if len(sent) != 1 || sent[0].Status != store.MessageCancelled || len(sent[0].Body.Texts) != 1 || sent[0].Body.Texts[0] != "要取消的" {
		t.Fatalf("sender history = %+v", sent)
	}
}

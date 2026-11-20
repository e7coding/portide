package node

import (
	"strings"
	"sync"
	"testing"
	"time"

	"portide/core/store"
)

type eventRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *eventRecorder) record(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *eventRecorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// pairWithEvents 和 pairOnline 一样，但两边都记录通知事件。
func pairWithEvents(t *testing.T) (laptop, desktop *testDevice, laptopEvents, desktopEvents *eventRecorder) {
	t.Helper()
	laptop, desktop = newDevice(t, "笔记本"), newDevice(t, "台式机")
	laptopEvents, desktopEvents = &eventRecorder{}, &eventRecorder{}
	laptop.n.opts.OnEvent = laptopEvents.record
	desktop.n.opts.OnEvent = desktopEvents.record
	pairDevices(t, laptop, desktop)
	return laptop, desktop, laptopEvents, desktopEvents
}

func TestReceiveEmitsEvent(t *testing.T) {
	laptop, desktop, laptopEvents, desktopEvents := pairWithEvents(t)
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "今晚八点开会", nil))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendDone)
	eventually(t, "received event", func() bool { return len(desktopEvents.all()) == 1 })
	e := desktopEvents.all()[0]
	got, _ := desktop.n.History("", 10)
	if e.Kind != EventReceived || e.PeerName != "笔记本" || e.Summary != "今晚八点开会" || e.Files != 0 || len(got) != 1 || e.HistoryID != got[0].ID {
		t.Fatalf("event = %+v, history = %+v", e, got)
	}
	time.Sleep(200 * time.Millisecond)
	if ev := laptopEvents.all(); len(ev) != 0 {
		t.Fatalf("a successful send was notified: %+v", ev)
	}
}

func TestAskAndRejectEmitEvents(t *testing.T) {
	laptop, desktop, laptopEvents, desktopEvents := pairWithEvents(t)
	if err := desktop.n.SetReceivePolicy(ReceivePolicy{Mode: PolicyAsk}); err != nil {
		t.Fatal(err)
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "", map[string]string{"a.txt": "x", "b.txt": "y"}))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "ask event", func() bool { return len(desktopEvents.all()) == 1 })
	ask := desktopEvents.all()[0]
	pending, _ := desktop.n.Store().PendingInbox(time.Now())
	if ask.Kind != EventAsk || ask.PeerName != "笔记本" || len(pending) != 1 || ask.InboxID != pending[0].ID {
		t.Fatalf("ask event = %+v, inbox = %+v", ask, pending)
	}
	if err := desktop.n.Store().DecideInbox(pending[0].ID, false); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendFailed)
	eventually(t, "send-failed event", func() bool { return len(laptopEvents.all()) == 1 })
	f := laptopEvents.all()[0]
	if f.Kind != EventSendFailed || f.PeerName != "台式机" || f.Error != "对方拒绝了" || !strings.HasSuffix(f.Summary, "等 2 个文件") {
		t.Fatalf("send-failed event = %+v", f)
	}
}

func TestNotifySwitch(t *testing.T) {
	laptop, desktop, _, desktopEvents := pairWithEvents(t)
	if !desktop.n.Notify() {
		t.Fatal("notify should default to on")
	}
	if err := desktop.n.SetNotify(false); err != nil {
		t.Fatal(err)
	}
	if desktop.n.Notify() {
		t.Fatal("notify still on")
	}
	id, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, plan(t, "不通知", nil))
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, laptop, id, store.SendDone)
	eventually(t, "received", func() bool { got, _ := desktop.n.History("", 10); return len(got) == 1 })
	time.Sleep(200 * time.Millisecond)
	if ev := desktopEvents.all(); len(ev) != 0 {
		t.Fatalf("events while notify is off: %+v", ev)
	}
}

// TestHistoryEventPartialSend 覆盖 spec 3.4：发出的消息只送达了一部分文件，
// 也要算作没有送达，发出 EventSendFailed（而不是悄悄没有通知）。
func TestHistoryEventPartialSend(t *testing.T) {
	d := newDevice(t, "笔记本")
	rec := &eventRecorder{}
	d.n.opts.OnEvent = rec.record

	d.n.historyEvent(1, store.MessageOut, store.MessagePartial, HistoryBody{PeerName: "台式机", Error: "对方停止了接收", Files: []HistoryFile{{Path: "a.txt"}, {Path: "b.txt"}}})
	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("events = %+v, want 1 EventSendFailed", got)
	}
	if got[0].Kind != EventSendFailed || got[0].Error != "对方停止了接收" {
		t.Fatalf("event = %+v, want Kind=%q Error=%q", got[0], EventSendFailed, "对方停止了接收")
	}
}

// TestHistoryEventDoneSend 发送完全成功时不通知。
func TestHistoryEventDoneSend(t *testing.T) {
	d := newDevice(t, "笔记本")
	rec := &eventRecorder{}
	d.n.opts.OnEvent = rec.record

	d.n.historyEvent(1, store.MessageOut, store.MessageDone, HistoryBody{PeerName: "台式机"})
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("events = %+v, want none for a fully-delivered send", got)
	}
}

// TestHistoryEventPartialReceive 收到的消息只到了一部分，仍然算收到，发 EventReceived。
func TestHistoryEventPartialReceive(t *testing.T) {
	d := newDevice(t, "笔记本")
	rec := &eventRecorder{}
	d.n.opts.OnEvent = rec.record

	d.n.historyEvent(1, store.MessageIn, store.MessagePartial, HistoryBody{PeerName: "台式机", Files: []HistoryFile{{Path: "a.txt"}}})
	got := rec.all()
	if len(got) != 1 || got[0].Kind != EventReceived {
		t.Fatalf("events = %+v, want 1 EventReceived", got)
	}
}

func TestBodySummary(t *testing.T) {
	long := strings.Repeat("字", notifyPreviewRunes+5)
	for _, c := range []struct {
		body HistoryBody
		want string
	}{
		{HistoryBody{Files: []HistoryFile{{Path: "照片/a.jpg"}}}, "a.jpg"},
		{HistoryBody{Files: []HistoryFile{{Path: "照片/a.jpg"}, {Path: "b.jpg"}, {Path: "c.jpg"}}}, "a.jpg 等 3 个文件"},
		{HistoryBody{Texts: []string{"你好"}}, "你好"},
		{HistoryBody{Texts: []string{long}}, strings.Repeat("字", notifyPreviewRunes) + "…"},
		{HistoryBody{}, ""},
	} {
		if got := bodySummary(c.body); got != c.want {
			t.Errorf("bodySummary(%+v) = %q, want %q", c.body, got, c.want)
		}
	}
}

package app

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"portide/core/node"
	"portide/core/store"
)

// pairedServices 返回已经组成设备圈、互相在线的笔记本和台式机（都经服务层完成配对）。
func pairedServices(t *testing.T) (laptop, desktop *Service) {
	t.Helper()
	useFileKeystore(t)
	laptop = runningService(t, "笔记本")
	p, err := laptop.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // 等守护进程的下一次轮询开放配对
	ln, _ := laptop.node()
	desktop = runningService(t, "台式机")
	if _, err := desktop.Join(p.Code, ln.Addr()); err != nil {
		t.Fatal(err)
	}
	isolateSaveDir(t, desktop)
	eventually(t, "both online", func() bool { return online(t, laptop, "台式机") && online(t, desktop, "笔记本") })
	return laptop, desktop
}

func online(t *testing.T, s *Service, name string) bool {
	t.Helper()
	devs, err := s.Devices()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devs {
		if d.Name == name {
			return d.Online
		}
	}
	return false
}

func keyOf(t *testing.T, s *Service) string {
	t.Helper()
	return s.State().KeyID
}

func timeline(t *testing.T, s *Service) []TimelineItem {
	t.Helper()
	items, err := s.Timeline("", 50)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// 发文本和文件：两边的时间线里都出现对应的条目，收到的文件能打开、能在文件夹里显示。
func TestSendShowsUpOnBothTimelines(t *testing.T) {
	laptop, desktop := pairedServices(t)
	var opened, revealed string
	desktop.cfg.Open = func(p string) error { opened = p; return nil }
	desktop.cfg.Reveal = func(p string) error { revealed = p; return nil }

	if _, err := laptop.SendText([]string{keyOf(t, desktop)}, "你好"); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "报告.txt")
	if err := os.WriteFile(src, []byte("内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := laptop.SendFiles([]string{keyOf(t, desktop)}, []string{src})
	if err != nil || res.Tasks != 1 {
		t.Fatalf("SendFiles = %+v, %v", res, err)
	}

	eventually(t, "desktop received both", func() bool { return len(timeline(t, desktop)) == 2 })
	// 两个任务分别发送，先后顺序不固定：按内容找。
	var text, file TimelineItem
	for _, it := range timeline(t, desktop) {
		if len(it.Files) > 0 {
			file = it
		} else {
			text = it
		}
	}
	if text.Direction != store.MessageIn || text.PeerName != "笔记本" || len(text.Texts) != 1 || text.Texts[0] != "你好" {
		t.Fatalf("text item = %+v", text)
	}
	if file.Kind != KindMessage || len(file.Files) != 1 || file.Files[0].Name != "报告.txt" || !file.Files[0].Exists {
		t.Fatalf("file item = %+v", file)
	}
	if err := desktop.OpenFile(file.ID, 0); err != nil || filepath.Base(opened) != "报告.txt" {
		t.Fatalf("OpenFile: opened %q, err %v", opened, err)
	}
	if err := desktop.RevealFile(file.ID, 0); err != nil || revealed != opened {
		t.Fatalf("RevealFile: revealed %q, err %v", revealed, err)
	}
	if paths, err := desktop.HistoryFiles(file.ID); err != nil || len(paths) != 1 || paths[0] != opened {
		t.Fatalf("HistoryFiles = %q, %v; want [%q]", paths, err, opened)
	}
	if _, err := desktop.HistoryFiles(file.ID + 1000); err == nil {
		t.Fatal("HistoryFiles of a missing entry must fail")
	}
	if err := desktop.OpenFile(file.ID, 1); err == nil {
		t.Fatal("a file index out of range must fail")
	}
	if err := os.Remove(opened); err != nil {
		t.Fatal(err)
	}
	if err := desktop.OpenFile(file.ID, 0); err == nil || err.Error() != "文件已被移走或删除" {
		t.Fatalf("missing file: err = %v", err)
	}
	if paths, _ := desktop.HistoryFiles(file.ID); len(paths) != 1 || paths[0] != "" {
		t.Fatalf("HistoryFiles after removal = %q", paths)
	}

	eventually(t, "laptop history has both", func() bool {
		items := timeline(t, laptop)
		return len(items) == 2 && items[0].Kind == KindMessage && items[1].Kind == KindMessage
	})
	for _, it := range timeline(t, laptop) {
		if it.Direction != store.MessageOut || it.Status != store.MessageDone {
			t.Fatalf("laptop item = %+v", it)
		}
	}
}

// 对方离线时发送：时间线里是一个排队中的任务，可以取消；取消之后变成一条"已取消"的历史。
func TestQueuedTaskCanBeCancelled(t *testing.T) {
	laptop, desktop := pairedServices(t)
	target := keyOf(t, desktop)
	Stop(desktop)
	eventually(t, "desktop offline", func() bool { return !online(t, laptop, "台式机") })

	if _, err := laptop.SendText([]string{target}, "稍后送达"); err != nil {
		t.Fatal(err)
	}
	items := timeline(t, laptop)
	if len(items) != 1 || items[0].Kind != KindTask || items[0].Status != store.SendPending || items[0].PeerName != "台式机" || items[0].Summary == "" {
		t.Fatalf("timeline = %+v", items)
	}
	if err := laptop.CancelSend(items[0].ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cancelled task recorded", func() bool {
		items := timeline(t, laptop)
		return len(items) == 1 && items[0].Kind == KindMessage && items[0].Status == store.MessageCancelled
	})
	if err := laptop.CancelSend(items[0].ID); err == nil {
		t.Fatal("cancelling a finished task must fail")
	}
}

func TestSendValidatesTargets(t *testing.T) {
	laptop, _ := pairedServices(t)
	if _, err := laptop.SendText(nil, "x"); err == nil {
		t.Fatal("no target must fail")
	}
	if _, err := laptop.SendText([]string{keyOf(t, laptop)}, "x"); err == nil {
		t.Fatal("sending to this device itself must fail")
	}
	if _, err := laptop.SendText([]string{"NOT-A-MEMBER"}, "x"); err == nil {
		t.Fatal("a non-member target must fail")
	}
	if items := timeline(t, laptop); len(items) != 0 {
		t.Fatalf("failed sends must not leave tasks behind: %+v", items)
	}
}

// 接收策略为"都要确认"时：待确认的接收出现在收件箱里，接受之后收到。
func TestInboxAcceptDelivers(t *testing.T) {
	laptop, desktop := pairedServices(t)
	if err := desktop.SetReceivePolicy("ask", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.SendText([]string{keyOf(t, desktop)}, "要确认"); err != nil {
		t.Fatal(err)
	}
	var inbox []InboxItem
	eventually(t, "inbox entry", func() bool {
		var err error
		inbox, err = desktop.Inbox()
		return err == nil && len(inbox) == 1
	})
	if inbox[0].PeerName != "笔记本" || inbox[0].ExpiresAt <= time.Now().UnixMilli() {
		t.Fatalf("inbox = %+v", inbox)
	}
	if err := desktop.DecideInbox(inbox[0].ID, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "received after accepting", func() bool { return len(timeline(t, desktop)) == 1 })
	if err := desktop.DecideInbox(inbox[0].ID, true); err == nil {
		t.Fatal("deciding twice must fail")
	}
}

// 多文件 / 文件夹时折叠显示（M3a 设计 3.1）：时间线只带前几个文件（也只检查它们在不在），另外给出文件总数。
func TestMessageItemCollapsesManyFiles(t *testing.T) {
	dir := t.TempDir()
	e := node.HistoryEntry{
		Message: store.Message{ID: 7, Direction: store.MessageIn, Status: store.MessageDone, Total: 7 * 3, CreatedAt: time.Now()},
		Body:    node.HistoryBody{PeerName: "笔记本", Dir: dir},
	}
	for i := range 7 {
		name := fmt.Sprintf("照片/%d.jpg", i)
		if err := os.MkdirAll(filepath.Join(dir, "照片"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte("abc"), 0o644); err != nil {
			t.Fatal(err)
		}
		e.Body.Files = append(e.Body.Files, node.HistoryFile{Path: name, Size: 3})
	}
	it := (&Service{}).messageItem(e, nil)
	if it.FileCount != 7 || len(it.Files) != TimelineFilesShown || it.BytesTotal != 21 {
		t.Fatalf("item = %+v", it)
	}
	for i, f := range it.Files {
		if f.Name != fmt.Sprintf("照片/%d.jpg", i) || !f.Exists {
			t.Fatalf("file %d = %+v", i, f)
		}
	}

	e.Body.Files = e.Body.Files[:2]
	if it := (&Service{}).messageItem(e, nil); it.FileCount != 2 || len(it.Files) != 2 {
		t.Fatalf("few files must all be shown: %+v", it)
	}
}

// 粘贴的图片先存进数据目录下的 pasted/，发送结束之后、下一次粘贴时清理；删除一条记录只删本机的。
func TestSendPastedAndDeleteHistory(t *testing.T) {
	laptop, desktop := pairedServices(t)
	target := keyOf(t, desktop)
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	if _, err := laptop.SendPasted([]string{target}, "截图.png", png); err != nil {
		t.Fatal(err)
	}
	received := func() []TimelineItem {
		var out []TimelineItem
		for _, it := range timeline(t, desktop) {
			if it.Kind == KindMessage && it.FileCount == 1 && it.Files[0].Name == "截图.png" && it.BytesTotal == 9 {
				out = append(out, it)
			}
		}
		return out
	}
	eventually(t, "desktop received the paste", func() bool { return len(received()) == 1 })
	eventually(t, "laptop recorded the send", func() bool { return len(timeline(t, laptop)) == 1 && timeline(t, laptop)[0].Kind == KindMessage })

	if _, err := laptop.SendPasted([]string{target}, "", png); err != nil { // 没给名字：用默认名
		t.Fatal(err)
	}
	subdirs, _ := os.ReadDir(laptop.pasteDir())
	if len(subdirs) != 1 {
		t.Fatalf("pasted dir holds %d copies, want only the pending one", len(subdirs))
	}
	if _, err := os.Stat(filepath.Join(laptop.pasteDir(), subdirs[0].Name(), "粘贴的图片.png")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "不是 base64!"} {
		if _, err := laptop.SendPasted([]string{target}, "x.png", bad); err == nil {
			t.Errorf("SendPasted(%q) accepted", bad)
		}
	}

	first := received()[0]
	if err := desktop.DeleteHistory(first.ID); err != nil {
		t.Fatal(err)
	}
	for _, it := range timeline(t, desktop) {
		if it.ID == first.ID && it.Kind == KindMessage {
			t.Fatal("the deleted entry is still on the timeline")
		}
	}
	if len(timeline(t, laptop)) == 0 {
		t.Fatal("deleting on one device must not touch the other's history")
	}
}

// 两次粘贴发送并发调用（Wails 对绑定方法就是这么调的）：对方离线、任务都排队着，
// 后一次的清理不能删掉前一次刚写进去的文件（M3c 复审修复）。
func TestSendPastedConcurrent(t *testing.T) {
	laptop, desktop := pairedServices(t)
	target := keyOf(t, desktop)
	Stop(desktop)
	eventually(t, "desktop offline", func() bool { return !online(t, laptop, "台式机") })

	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	const n = 2
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = laptop.SendPasted([]string{target}, fmt.Sprintf("截图%d.png", i), png)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("SendPasted %d: %v", i, err)
		}
	}

	subdirs, err := os.ReadDir(laptop.pasteDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(subdirs) != n {
		t.Fatalf("pasted dir holds %d copies, want %d (both still pending)", len(subdirs), n)
	}
	names := map[string]bool{}
	for _, sub := range subdirs {
		entries, err := os.ReadDir(filepath.Join(laptop.pasteDir(), sub.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			names[e.Name()] = true
		}
	}
	for i := range n {
		want := fmt.Sprintf("截图%d.png", i)
		if !names[want] {
			t.Errorf("missing %q after concurrent pastes, have %v", want, names)
		}
	}
}

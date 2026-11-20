package app

import (
	"encoding/json"
	"testing"

	"portide/core/node"
	"portide/core/store"
)

func TestPreview(t *testing.T) {
	files := func(paths ...string) []node.HistoryFile {
		var out []node.HistoryFile
		for _, p := range paths {
			out = append(out, node.HistoryFile{Path: p})
		}
		return out
	}
	long := "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十多出来"
	for _, c := range []struct {
		body node.HistoryBody
		want string
	}{
		{node.HistoryBody{Texts: []string{"  第一行\n第二行"}}, "第一行"},
		{node.HistoryBody{Texts: []string{long}}, long[:len("一二三四五六七八九十")*6] + "…"},
		{node.HistoryBody{Files: files("照片/a.jpg")}, "a.jpg"},
		{node.HistoryBody{Files: files("照片/a.jpg", "照片/b.jpg")}, "照片（2 个文件）"},
		{node.HistoryBody{Files: files("a.jpg", "b.pdf", "c.txt")}, "3 个文件"},
		{node.HistoryBody{Summary: "季度报告.pdf（2 MB）", Error: "已停止接收"}, "季度报告.pdf（2 MB）"},
		{node.HistoryBody{}, ""},
	} {
		if got := preview(c.body); got != c.want {
			t.Errorf("preview(%+v) = %q, want %q", c.body, got, c.want)
		}
	}
}

// 会话列表："全部设备"在最前，每台其他设备带最后一条历史的摘要、方向和排队数。
func TestConversations(t *testing.T) {
	laptop, desktop := pairedServices(t)
	if _, err := laptop.SendText([]string{keyOf(t, desktop)}, "第一行\n第二行"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "desktop received", func() bool { return len(timeline(t, desktop)) == 1 })
	convs, err := desktop.Conversations()
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 2 || convs[0].Key != AllDevices || convs[0].Name != "全部设备" || convs[0].LastPreview != "第一行" {
		t.Fatalf("conversations = %+v", convs)
	}
	if c := convs[1]; c.Key != keyOf(t, laptop) || c.Name != "笔记本" || c.Icon != IconLaptop || !c.Online || c.Path != PathLAN ||
		c.LastPreview != "第一行" || c.LastDirection != store.MessageIn || c.LastAt == 0 || c.Queued != 0 {
		t.Fatalf("laptop conversation = %+v", c)
	}

	eventually(t, "laptop finished the first send", func() bool { list, err := laptop.Transfers(); return err == nil && list.Count() == 0 })
	desktopKey := keyOf(t, desktop)
	Stop(desktop) // 对方离线：再发的一条排队，排队数算进这台设备和"全部设备"
	eventually(t, "desktop offline", func() bool { return !online(t, laptop, "台式机") })
	if _, err := laptop.SendText([]string{desktopKey}, "稍后送达"); err != nil {
		t.Fatal(err)
	}
	convs, _ = laptop.Conversations()
	if len(convs) != 2 || convs[0].Queued != 1 || convs[1].Queued != 1 || convs[1].LastDirection != store.MessageOut {
		t.Fatalf("laptop conversations = %+v", convs)
	}
}

// addEntry 直接往历史里写一条（测试搜索和缩略图用），返回编号。
func addEntry(t *testing.T, s *Service, direction string, body node.HistoryBody) int64 {
	t.Helper()
	n, err := s.node()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(body)
	id, err := n.Store().AddMessage(store.Message{Direction: direction, PeerKey: "PEER", Status: store.MessageDone, Body: b, Files: len(body.Files)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

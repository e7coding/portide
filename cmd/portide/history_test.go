package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"portide/core/node"
	"portide/core/store"
)

func entry(dir, status string, body node.HistoryBody) node.HistoryEntry {
	e := node.HistoryEntry{Body: body}
	e.ID, e.Direction, e.Status = 7, dir, status
	e.CreatedAt = time.Date(2026, 10, 23, 9, 15, 2, 0, time.Local)
	for _, f := range body.Files {
		e.Files++
		e.Total += int64(f.Size)
	}
	e.Texts = len(body.Texts)
	return e
}

func TestHistorySummaryAndStatus(t *testing.T) {
	text := entry(store.MessageIn, store.MessageDone, node.HistoryBody{PeerName: "手机", Texts: []string{"今晚\n八点  开会，记得带上周报和投影仪的转接头哦", "第二段"}})
	files := entry(store.MessageOut, store.MessagePartial, node.HistoryBody{PeerName: "台式机",
		Files: []node.HistoryFile{{Path: "照片/1.jpg", Size: 1024}, {Path: "照片/2.jpg", Size: 1024}}, Texts: []string{"看看"}})
	for _, c := range []struct {
		e                node.HistoryEntry
		dir, sum, status string
	}{
		{text, "← 来自", "文本「今晚 八点 开会，记得带上周报和投影仪的转接头哦」 等 2 段", "已收到"},
		{files, "→ 发给", "照片/1.jpg 等 2 个文件（2.0 KB） 和 1 段文本", "部分送达"},
		{entry(store.MessageOut, store.MessageDone, node.HistoryBody{}), "→ 发给", "（无内容）", "已送达"},
		{entry(store.MessageOut, store.MessageFailed, node.HistoryBody{}), "→ 发给", "（无内容）", "失败"},
		{entry(store.MessageIn, store.MessageCancelled, node.HistoryBody{}), "← 来自", "（无内容）", "已取消"},
	} {
		if d, s, st := historyDirection(c.e), historySummary(c.e), historyStatus(c.e); d != c.dir || s != c.sum || st != c.status {
			t.Errorf("got %q %q %q; want %q %q %q", d, s, st, c.dir, c.sum, c.status)
		}
	}
}

func TestShowHistory(t *testing.T) {
	in := entry(store.MessageIn, store.MessageDone, node.HistoryBody{PeerName: "笔记本", Dir: "/home/me/Downloads/Portide",
		Texts: []string{"第一行\n第二行"}, Files: []node.HistoryFile{{Path: "资料/a.txt", Size: 3}}})
	var b bytes.Buffer
	showHistory(&b, &in)
	want := "编号：7\n时间：2026-10-23 09:15:02\n来自：笔记本\n结果：已收到\n文本 1：\n第一行\n第二行\n文件（1 个，3 B）：\n  " +
		filepath.Join("/home/me/Downloads/Portide", "资料", "a.txt") + "\n"
	if b.String() != want {
		t.Fatalf("show =\n%s\nwant\n%s", b.String(), want)
	}
	out := entry(store.MessageOut, store.MessageFailed, node.HistoryBody{PeerName: "台式机", Error: "对方拒绝了",
		Files: []node.HistoryFile{{Path: "a.txt", Size: 3, Local: "/src/a.txt"}, {Path: "文本-1.txt", Size: 70000}}})
	b.Reset()
	showHistory(&b, &out)
	want = "编号：7\n时间：2026-10-23 09:15:02\n发给：台式机\n结果：失败\n原因：对方拒绝了\n文件（2 个，68.4 KB）：\n  /src/a.txt\n  文本-1.txt\n"
	if b.String() != want {
		t.Fatalf("show =\n%s\nwant\n%s", b.String(), want)
	}

	// Test that escape sequences in text are escaped
	dangerous := entry(store.MessageIn, store.MessageDone, node.HistoryBody{PeerName: "笔记本",
		Texts: []string{"clear screen: \x1b[2J should be escaped"}})
	b.Reset()
	showHistory(&b, &dangerous)
	if !strings.Contains(b.String(), `\x1b[2J`) {
		t.Fatalf("escape sequence not escaped in output: %q", b.String())
	}
}

func TestDescribeHistoryDays(t *testing.T) {
	if describeHistoryDays(0) != "永久" || describeHistoryDays(30) != "30 天（更早的自动删除）" {
		t.Fatal(describeHistoryDays(0), describeHistoryDays(30))
	}
}

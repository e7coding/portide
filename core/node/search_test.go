package node

import (
	"encoding/json"
	"testing"
	"time"

	"portide/core/store"
)

func addBody(t testing.TB, d *testDevice, body HistoryBody, at time.Time) int64 {
	t.Helper()
	b, _ := json.Marshal(body)
	id, err := d.n.Store().AddMessage(store.Message{Direction: store.MessageIn, PeerKey: "P", Status: store.MessageDone, Body: b, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSearchHistory(t *testing.T) {
	d := newDevice(t, "笔记本")
	now := time.Now()
	text := addBody(t, d, HistoryBody{PeerName: "手机", Texts: []string{"会议纪要在 Wiki 上"}}, now.Add(-3*time.Minute))
	file := addBody(t, d, HistoryBody{PeerName: "手机", Files: []HistoryFile{{Path: "照片/WIKI-封面.png", Size: 1}}}, now.Add(-2*time.Minute))
	addBody(t, d, HistoryBody{PeerName: "手机", Files: []HistoryFile{{Path: "wiki/a.txt", Size: 1}}}, now.Add(-time.Minute)) // 只有目录名匹配
	stopped := addBody(t, d, HistoryBody{PeerName: "手机", Summary: "Wiki 导出.zip（3 MB）", Error: "已停止接收"}, now)

	got, err := d.n.SearchHistory("  wiki ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != stopped || got[1].ID != file || got[2].ID != text {
		t.Fatalf("SearchHistory = %+v, want stopped, file, text (newest first)", got)
	}
	if got, _ := d.n.SearchHistory("wiki", 1); len(got) != 1 || got[0].ID != stopped {
		t.Fatalf("limit 1 = %+v", got)
	}
	for _, q := range []string{"", "  ", "不存在"} {
		if got, _ := d.n.SearchHistory(q, 10); len(got) != 0 {
			t.Errorf("SearchHistory(%q) = %d results, want none", q, len(got))
		}
	}
}

// 设计要求：1 万条历史一次搜索在 300ms 以内（否则考虑加索引）。
func BenchmarkSearchHistory10k(b *testing.B) {
	d := newDevice(b, "笔记本")
	base := time.Now().Add(-time.Hour)
	for i := range 10000 {
		addBody(b, d, HistoryBody{PeerName: "手机", Texts: []string{"一段普通的聊天内容 hello world"}, Files: []HistoryFile{{Path: "文件夹/报告.pdf", Size: 1}}}, base.Add(time.Duration(i)*time.Millisecond))
	}
	b.ResetTimer()
	for range b.N {
		if got, _ := d.n.SearchHistory("不会出现的词", 10); len(got) != 0 {
			b.Fatal("unexpected hit")
		}
	}
}

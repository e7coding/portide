package app

import (
	"testing"

	"portide/core/node"
	"portide/core/store"
)

func TestSnippet(t *testing.T) {
	for _, c := range []struct{ text, q, want string }{
		{"会议纪要在 Wiki 上", "wiki", "会议纪要在 Wiki 上"},
		{"前面有很多很多很多很多很多的文字，然后才说到 Wiki\n下一行", "wiki", "…很多的文字，然后才说到 Wiki 下一行"},
		{"没有", "wiki", ""},
	} {
		got, ok := snippet(c.text, c.q)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("snippet(%q, %q) = %q %v, want %q", c.text, c.q, got, ok, c.want)
		}
	}
}

func TestSearch(t *testing.T) {
	useFileKeystore(t)
	s := runningService(t, "笔记本")
	text := addEntry(t, s, store.MessageIn, node.HistoryBody{PeerName: "手机", Texts: []string{"会议纪要在 Wiki 上"}})
	file := addEntry(t, s, store.MessageOut, node.HistoryBody{PeerName: "手机", Files: []node.HistoryFile{{Path: "照片/a.jpg"}, {Path: "照片/WIKI-封面.png"}}})

	got, err := s.Search("wiki")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Search = %+v", got)
	}
	byID := map[int64]SearchResult{got[0].HistoryID: got[0], got[1].HistoryID: got[1]}
	if r := byID[text]; r.Snippet != "会议纪要在 Wiki 上" || r.FileIndex != -1 || r.PeerName != "手机" || r.Direction != store.MessageIn {
		t.Fatalf("text result = %+v", r)
	}
	if r := byID[file]; r.Snippet != "WIKI-封面.png" || r.FileIndex != 1 || r.Direction != store.MessageOut {
		t.Fatalf("file result = %+v", r)
	}
	if got, err := s.Search(" "); err != nil || len(got) != 0 {
		t.Fatalf("blank query = %+v %v", got, err)
	}
}

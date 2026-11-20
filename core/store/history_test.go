package store

import (
	"errors"
	"testing"
	"time"
)

func TestMessages(t *testing.T) {
	s := open(t)
	if err := s.UseKey(testKey(1)); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	add := func(dir, peer, status string, minute int, body string) int64 {
		id, err := s.AddMessage(Message{Direction: dir, PeerKey: peer, Status: status, Files: 1, Total: 10, Body: []byte(body), CreatedAt: base.Add(time.Duration(minute) * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := add(MessageIn, "A", MessageDone, 0, "来自 A 的照片.jpg")
	b := add(MessageOut, "B", MessagePartial, 1, "发给 B 的报告.pdf")
	c := add(MessageIn, "A", MessageDone, 2, "来自 A 的文本")

	if rawContains(t, s, "照片") || rawContains(t, s, "报告") {
		t.Fatal("history body stored in plaintext")
	}
	all, err := s.ListMessages("", 10)
	if err != nil || len(all) != 3 || all[0].ID != c || all[2].ID != a || string(all[1].Body) != "发给 B 的报告.pdf" {
		t.Fatalf("ListMessages = %+v, %v", all, err)
	}
	if fromA, _ := s.ListMessages("A", 10); len(fromA) != 2 || fromA[0].ID != c {
		t.Fatalf("filtered = %+v", fromA)
	}
	if latest, _ := s.ListMessages("", 1); len(latest) != 1 || latest[0].ID != c {
		t.Fatalf("limit = %+v", latest)
	}
	m, err := s.GetMessage(b)
	if err != nil || m.Direction != MessageOut || m.Status != MessagePartial || !m.CreatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("GetMessage = %+v, %v", m, err)
	}
	if err := s.DeleteMessage(b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMessage(b); !errors.Is(err, ErrNoMessage) {
		t.Fatalf("deleted message: %v", err)
	}
	if err := s.DeleteMessage(b); !errors.Is(err, ErrNoMessage) {
		t.Fatalf("delete twice: %v", err)
	}
	_ = s.PruneMessages(base.Add(time.Minute))
	if left, _ := s.ListMessages("", 10); len(left) != 1 || left[0].ID != c {
		t.Fatalf("after prune = %+v", left)
	}
	if n, err := s.ClearMessages(); err != nil || n != 1 {
		t.Fatalf("ClearMessages = %d, %v", n, err)
	}
}

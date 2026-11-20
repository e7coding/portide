package store

import (
	"reflect"
	"testing"
	"time"
)

func TestCircleMoveRoundTrip(t *testing.T) {
	s := open(t)
	if m, err := s.CircleMove(); err != nil || m != nil {
		t.Fatalf("empty CircleMove = %v, %v", m, err)
	}
	want := &CircleMove{
		OldCircle: "circle-a",
		OldChain:  [][]byte{{1, 2, 3}, {4, 5}},
		Brought:   []string{"k1", "k2"},
		Pending:   []string{"k3", "k4"},
		Elsewhere: []string{"k5", "k6"},
		ExpiresAt: time.Unix(1700000000, 0),
	}
	if err := s.SaveCircleMove(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.CircleMove()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("CircleMove = %+v, %v; want %+v", got, err, want)
	}
	next := &CircleMove{OldCircle: "circle-b", ExpiresAt: time.Unix(1800000000, 0)}
	if err := s.SaveCircleMove(next); err != nil {
		t.Fatal(err)
	}
	got, err = s.CircleMove()
	if err != nil || got == nil || got.OldCircle != "circle-b" || got.OldChain != nil ||
		len(got.Brought) != 0 || len(got.Pending) != 0 || len(got.Elsewhere) != 0 || !got.ExpiresAt.Equal(next.ExpiresAt) {
		t.Fatalf("replaced CircleMove = %+v, %v", got, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM circle_moves`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, %v", n, err)
	}
	if err := s.DeleteCircleMove(); err != nil {
		t.Fatal(err)
	}
	if m, err := s.CircleMove(); err != nil || m != nil {
		t.Fatalf("after delete = %v, %v", m, err)
	}
}

func TestCompletePairSessionBrought(t *testing.T) {
	s := open(t)
	id, _ := s.CreatePairSession("11112222", time.Minute)
	if err := s.CompletePairSession(id, "电脑", "merge", 2); err != nil {
		t.Fatal(err)
	}
	ps, err := s.GetPairSession(id)
	if err != nil || ps.Brought != 2 || ps.Mode != "merge" {
		t.Fatalf("session = %+v, %v", ps, err)
	}
}

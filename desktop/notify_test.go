package main

import "testing"

func TestParseNoteID(t *testing.T) {
	for id, want := range map[string]struct {
		kind byte
		num  int64
	}{
		"h12": {'h', 12}, "a3": {'a', 3}, "f7": {'f', 7}, "hint": {0, 0}, "h": {0, 0}, "": {0, 0},
	} {
		if kind, num := parseNoteID(id); kind != want.kind || num != want.num {
			t.Errorf("parseNoteID(%q) = %c %d, want %c %d", id, kind, num, want.kind, want.num)
		}
	}
}

func TestFirstHide(t *testing.T) {
	dir := t.TempDir()
	if !firstHide(dir) {
		t.Fatal("first hide not reported")
	}
	if firstHide(dir) {
		t.Fatal("second hide reported again")
	}
}

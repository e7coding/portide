package app

import (
	"slices"
	"testing"
)

func TestSuggestNames(t *testing.T) {
	for _, c := range []struct {
		host, user string
		battery    bool
		want       []string
	}{
		{"LAPTOP-8F2K1", "joy", false, []string{"joy 的笔记本", "joy 的台式机", "LAPTOP-8F2K1"}},
		{"DESKTOP-ABC", "joy", true, []string{"joy 的台式机", "joy 的笔记本", "DESKTOP-ABC"}},
		{"joy-ThinkPad-X1", "joy", false, []string{"joy 的笔记本", "joy 的台式机", "joy-ThinkPad-X1"}},
		{"nas", "joy", true, []string{"joy 的笔记本", "joy 的台式机", "nas"}},
		{"", "", false, []string{"台式机", "笔记本"}},
	} {
		if got := suggestNames(c.host, c.user, c.battery); !slices.Equal(got, c.want) {
			t.Errorf("suggestNames(%q, %q, %v) = %q, want %q", c.host, c.user, c.battery, got, c.want)
		}
	}
}

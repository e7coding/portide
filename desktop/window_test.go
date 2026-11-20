package main

import (
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestWindowStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if st, saved := loadWindowState(dir); saved || st.Width != defaultWidth || st.Height != defaultHeight {
		t.Fatalf("no saved state: %+v %v", st, saved)
	}
	saveWindowState(dir, windowState{X: 10, Y: 20, Width: 900, Height: 600, Maximised: true})
	if st, saved := loadWindowState(dir); !saved || st != (windowState{X: 10, Y: 20, Width: 900, Height: 600, Maximised: true}) {
		t.Fatalf("loaded %+v %v", st, saved)
	}
	saveWindowState(dir, windowState{Width: 0, Height: 0}) // 最小化时的尺寸：不覆盖
	if st, _ := loadWindowState(dir); st.Width != 900 {
		t.Fatalf("a zero size overwrote the saved state: %+v", st)
	}
}

func TestOnScreen(t *testing.T) {
	screens := []application.Rect{{X: 0, Y: 0, Width: 1920, Height: 1040}, {X: 1920, Y: 0, Width: 1280, Height: 1000}}
	for _, c := range []struct {
		st   windowState
		want bool
	}{
		{windowState{X: 100, Y: 100, Width: 1000, Height: 700}, true},
		{windowState{X: 2000, Y: 50, Width: 1000, Height: 700}, true},    // 第二块屏幕上
		{windowState{X: 3150, Y: 50, Width: 1000, Height: 700}, false},   // 只露出 50 像素
		{windowState{X: -3000, Y: 100, Width: 1000, Height: 700}, false}, // 拔掉的外接屏上
	} {
		if got := onScreen(c.st, screens); got != c.want {
			t.Errorf("onScreen(%+v) = %v, want %v", c.st, got, c.want)
		}
	}
}

func TestStopWithin(t *testing.T) {
	if !stopWithin(time.Second, func() {}) {
		t.Fatal("a quick stop must report done")
	}
	block := make(chan struct{})
	defer close(block)
	if stopWithin(20*time.Millisecond, func() { <-block }) {
		t.Fatal("a stuck stop must report not done")
	}
}

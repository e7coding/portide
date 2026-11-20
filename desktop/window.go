package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 窗口状态（M3c 设计 5.4）：退出或缩到托盘时记下窗口的大小和位置，下次启动时恢复。
const (
	windowStateFile = "desktop-window.json"
	defaultWidth    = 1080
	defaultHeight   = 720
	minWidth        = 860
	minHeight       = 560
	// visibleMargin：窗口至少要有这么宽、这么高的一块落在某个屏幕的工作区里，才按记下的位置放；
	// 否则（例如外接屏拔掉了）居中显示。
	visibleMargin = 120
)

// windowState 是记在数据目录里的窗口状态。
type windowState struct {
	X, Y, Width, Height int
	Maximised           bool
}

func loadWindowState(dataDir string) (windowState, bool) {
	var st windowState
	b, err := os.ReadFile(filepath.Join(dataDir, windowStateFile))
	if err != nil || json.Unmarshal(b, &st) != nil || st.Width < minWidth || st.Height < minHeight {
		return windowState{Width: defaultWidth, Height: defaultHeight}, false
	}
	return st, true
}

func saveWindowState(dataDir string, st windowState) {
	if st.Width < minWidth || st.Height < minHeight {
		return // 最小化时读到的尺寸可能是 0：不记
	}
	b, _ := json.Marshal(st)
	_ = os.WriteFile(filepath.Join(dataDir, windowStateFile), b, 0o600)
}

// captureWindow 读出窗口当前的状态；最大化时保留上一次记下的普通大小和位置。
func captureWindow(w *application.WebviewWindow, prev windowState) windowState {
	if w.IsMinimised() {
		return prev
	}
	if w.IsMaximised() {
		prev.Maximised = true
		return prev
	}
	b := w.Bounds()
	return windowState{X: b.X, Y: b.Y, Width: b.Width, Height: b.Height}
}

// onScreen 报告窗口 st 是否有足够大的一块落在某个工作区里。
func onScreen(st windowState, areas []application.Rect) bool {
	for _, a := range areas {
		w := min(st.X+st.Width, a.X+a.Width) - max(st.X, a.X)
		h := min(st.Y+st.Height, a.Y+a.Height) - max(st.Y, a.Y)
		if w >= visibleMargin && h >= visibleMargin/2 {
			return true
		}
	}
	return false
}

// restoreWindow 在应用启动之后（这时才能读到屏幕信息）把窗口放回记下的位置；位置不在任何屏幕上时居中。
func restoreWindow(wapp *application.App, w *application.WebviewWindow, st windowState, saved bool) {
	var areas []application.Rect
	for _, s := range wapp.Screen.GetAll() {
		areas = append(areas, s.WorkArea)
	}
	switch {
	case saved && onScreen(st, areas):
		w.SetPosition(st.X, st.Y)
	default:
		w.Center()
	}
	if st.Maximised {
		w.Maximise()
	}
}

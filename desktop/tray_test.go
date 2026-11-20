package main

import (
	"testing"

	"portide/app"
)

func TestTrayView(t *testing.T) {
	sending := app.TransferItem{Status: "sending", Summary: "季度报告.pdf", BytesDone: 30, BytesTotal: 100}
	queued := app.TransferItem{Status: "pending", Summary: "a.txt", BytesTotal: 500}
	paused := app.TransferItem{Status: "sending", Paused: true, BytesDone: 10, BytesTotal: 1000}
	receiving := app.TransferItem{Status: "receiving", Summary: "照片（9 个文件）", BytesDone: 60, BytesTotal: 100}
	for _, c := range []struct {
		name   string
		list   app.TransferList
		online int
		want   trayView
	}{
		{"idle", app.TransferList{}, 0, trayView{status: "空闲 · 没有设备在线"}},
		{"queued only", app.TransferList{Outgoing: []app.TransferItem{queued}}, 1,
			trayView{status: "1 个等待中 · 1 台设备在线", canPause: true}},
		{"sending and receiving", app.TransferList{Outgoing: []app.TransferItem{queued, sending}, Incoming: []app.TransferItem{receiving}, Active: 2}, 2,
			trayView{status: "2 个正在传输 · 2 台设备在线", detail: "季度报告.pdf · 30%", active: true, canPause: true}},
		{"receiving only", app.TransferList{Incoming: []app.TransferItem{receiving}, Active: 1}, 1,
			trayView{status: "1 个正在传输 · 1 台设备在线", detail: "照片（9 个文件） · 60%", active: true}},
		{"all paused", app.TransferList{Outgoing: []app.TransferItem{paused}}, 1,
			trayView{status: "1 个等待中 · 1 台设备在线", canPause: true, allPaused: true}},
	} {
		if got := viewOf(c.list, c.online); got != c.want {
			t.Errorf("%s: viewOf = %+v, want %+v", c.name, got, c.want)
		}
	}
}

package main

import (
	"testing"

	"portide/core/node"
)

func TestMergeResultText(t *testing.T) {
	got := mergeResultText([]string{"X", "Y", "A1", "A2"}, []string{"A1", "A2"}, []string{"A2"})
	want := "✓ 已合并，设备圈里现在有 4 台设备：X、Y、A1、A2\n" +
		"还没跟过来：「A1」「A2」（需要先升级）——它们下次和这里任意一台合并过来的设备连上时会自动跟过来（30 天内）\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// 同名的两台里只有一台需要升级：只有一个名字带说明。
	got = mergeResultText([]string{"X", "P", "P"}, []string{"P", "P"}, []string{"P"})
	want = "✓ 已合并，设备圈里现在有 3 台设备：X、P、P\n" +
		"还没跟过来：「P」（需要先升级）「P」——它们下次和这里任意一台合并过来的设备连上时会自动跟过来（30 天内）\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// 同名的两台都需要升级：两个名字都带说明。
	got = mergeResultText([]string{"X", "P", "P"}, []string{"P", "P"}, []string{"P", "P"})
	want = "✓ 已合并，设备圈里现在有 3 台设备：X、P、P\n" +
		"还没跟过来：「P」（需要先升级）「P」（需要先升级）——它们下次和这里任意一台合并过来的设备连上时会自动跟过来（30 天内）\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := mergeResultText([]string{"X", "Y"}, nil, nil); got != "✓ 已合并，设备圈里现在有 2 台设备：X、Y\n" {
		t.Fatalf("no waiting: %q", got)
	}
}

func TestPairDoneText(t *testing.T) {
	for _, c := range []struct {
		mode    string
		brought int
		want    string
	}{
		{"merge", 2, "✓ 已合并：「手机」和它设备圈里的 2 台设备加入了设备圈\n"},
		{"adopt", 0, "✓ 本机已加入「手机」的设备圈\n"},
		{"join", 0, "✓「手机」已加入设备圈\n"},
	} {
		if got := pairDoneText(c.mode, "手机", c.brought); got != c.want {
			t.Errorf("%s: got %q want %q", c.mode, got, c.want)
		}
	}
}

func TestDeviceStatusText(t *testing.T) {
	for _, c := range []struct {
		d       node.DeviceStatus
		running bool
		want    string
	}{
		{node.DeviceStatus{Self: true}, true, "本机"},
		{node.DeviceStatus{Online: true}, true, "在线"},
		{node.DeviceStatus{}, true, "离线"},
		{node.DeviceStatus{}, false, "未知"},
		{node.DeviceStatus{Move: node.MoveWaiting}, true, "等待合并"},
		{node.DeviceStatus{Move: node.MoveOutdated}, true, "需要升级"},
		{node.DeviceStatus{Move: node.MoveLeft}, true, "没有跟过来"},
		{node.DeviceStatus{Move: node.MoveElsewhere}, true, "去了别的设备圈"},
	} {
		if got := deviceStatusText(c.d, c.running); got != c.want {
			t.Errorf("%+v: got %q want %q", c.d, got, c.want)
		}
	}
}

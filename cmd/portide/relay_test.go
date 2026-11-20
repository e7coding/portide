package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"portide/core/node"
	"portide/core/relay"
)

func TestDeviceAddr(t *testing.T) {
	for _, c := range []struct {
		d    node.DeviceStatus
		want string
	}{
		{node.DeviceStatus{Self: true, Addr: "1.2.3.4:7443"}, "-"},
		{node.DeviceStatus{Addr: ""}, "-"},
		{node.DeviceStatus{Addr: "relay"}, "经中继"},
		{node.DeviceStatus{Addr: "relay:1.2.3.4:7443"}, "经中继（1.2.3.4:7443）"},
		{node.DeviceStatus{Addr: "192.168.1.5:7443"}, "192.168.1.5:7443"},
	} {
		if got := deviceAddr(c.d); got != c.want {
			t.Errorf("deviceAddr(%+v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// 参数不对、配置串格式不对时在打开数据目录之前就报错。
func TestRelayArgs(t *testing.T) {
	dir := t.TempDir()
	if err := cmdRelay(dir, []string{"add", "https://example.com"}); err == nil || !strings.Contains(err.Error(), "配置串格式不对") {
		t.Fatalf("bad config: err = %v", err)
	}
	for _, args := range [][]string{{"add"}, {"rm", "x", "y"}, {"list"}} {
		if err := cmdRelay(dir, args); err == nil || !strings.HasPrefix(err.Error(), "用法：") {
			t.Errorf("cmdRelay(%q): err = %v", args, err)
		}
	}
}

// `portide relay add -` 从标准输入读配置串：格式不对时返回和直接传参一样的错误。
func TestRelayAddDashReadsStdin(t *testing.T) {
	dir := t.TempDir()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	if _, err := w.WriteString("不是配置串\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if err := cmdRelay(dir, []string{"add", "-"}); err == nil || !strings.Contains(err.Error(), "配置串格式不对") {
		t.Fatalf("add - with a malformed stdin line: err = %v", err)
	}
}

func testRelays() []relay.Config {
	a := relay.Config{Addr: "203.0.113.100:7443"}
	a.Fingerprint[0] = 0x8f
	b := relay.Config{Addr: "198.51.100.20:7443"}
	b.Fingerprint[0] = 0x9d
	return []relay.Config{a, b}
}

// rm 的参数：编号、地址；只有一个中继时可省略；多个时省略要报错并列出编号。
func TestPickRelay(t *testing.T) {
	two := testRelays()
	for _, c := range []struct {
		rs   []relay.Config
		arg  string
		want int
		err  string
	}{
		{two, "2", 1, ""},
		{two, "203.0.113.100:7443", 0, ""},
		{two[:1], "", 0, ""},
		{two, "", 0, "有多个中继，请指定编号或地址：\n1. 203.0.113.100:7443\n2. 198.51.100.20:7443"},
		{two, "3", 0, "没有这个中继"},
		{two, "0", 0, "没有这个中继"},
		{two, "nope:1", 0, "没有这个中继"},
		{nil, "", 0, "没有这个中继"},
	} {
		got, err := pickRelay(c.rs, c.arg)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("pickRelay(%d, %q): err = %v, want %q", len(c.rs), c.arg, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("pickRelay(%d, %q) = %d, %v, want %d", len(c.rs), c.arg, got, err, c.want)
		}
	}
}

func TestPrintRelayList(t *testing.T) {
	var buf bytes.Buffer
	printRelayList(&buf, testRelays(), func(fp [32]byte) string {
		if fp[0] == 0x8f {
			return "已连接"
		}
		return ""
	}, false)
	want := "1. 203.0.113.100:7443   指纹 8f00000000000000…   已连接\n2. 198.51.100.20:7443   指纹 9d00000000000000…   正在连接\n"
	if buf.String() != want {
		t.Fatalf("got %q", buf.String())
	}
	buf.Reset()
	printRelayList(&buf, testRelays()[:1], func([32]byte) string { return "已连接" }, true)
	if !strings.HasSuffix(buf.String(), "   守护进程未运行\n") {
		t.Fatalf("daemon down: %q", buf.String())
	}
}

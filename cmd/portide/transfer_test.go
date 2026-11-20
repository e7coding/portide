package main

import (
	"reflect"
	"testing"
	"time"

	"portide/core/node"
	"portide/core/store"
)

func TestParseSendArgs(t *testing.T) {
	for _, c := range []struct {
		name  string
		args  []string
		text  string
		paths []string
	}{
		{"text before paths", []string{"--text", "hi", "a.jpg", "b.jpg"}, "hi", []string{"a.jpg", "b.jpg"}},
		{"text after paths", []string{"a.jpg", "b.jpg", "--text", "hi"}, "hi", []string{"a.jpg", "b.jpg"}},
		{"text between paths", []string{"a.jpg", "--text", "hi", "b.jpg"}, "hi", []string{"a.jpg", "b.jpg"}},
		{"no text", []string{"a.jpg", "b.jpg"}, "", []string{"a.jpg", "b.jpg"}},
		{"only text", []string{"--text", "hi"}, "hi", nil},
		{"-- ends flag parsing", []string{"--", "-note.txt"}, "", []string{"-note.txt"}},
		{"-- after a path", []string{"a.jpg", "--", "-note.txt"}, "", []string{"a.jpg", "-note.txt"}},
		{"-- after --text", []string{"--text", "hi", "--", "-note.txt", "--text"}, "hi", []string{"-note.txt", "--text"}},
		{"--text=value", []string{"--text=你好", "a.jpg"}, "你好", []string{"a.jpg"}},
		{"single dash flag", []string{"-text", "hi"}, "hi", nil},
		{"text that looks like a flag", []string{"--text", "--", "a.jpg"}, "--", []string{"a.jpg"}},
		{"stdin dash is a path", []string{"-"}, "", []string{"-"}},
	} {
		text, paths, err := parseSendArgs(c.args)
		if err != nil || text != c.text || !reflect.DeepEqual(paths, c.paths) {
			t.Errorf("%s: parseSendArgs(%v) = %q, %v, %v; want %q, %v", c.name, c.args, text, paths, err, c.text, c.paths)
		}
	}
	for _, bad := range [][]string{{"--bogus", "x"}, {"a.jpg", "-note.txt"}, {"--text"}} {
		if _, _, err := parseSendArgs(bad); err == nil {
			t.Errorf("parseSendArgs(%v) accepted", bad)
		}
	}
}

func TestSplitTargets(t *testing.T) {
	for in, want := range map[string][]string{
		"台式机":      {"台式机"},
		"台式机,手机":   {"台式机", "手机"},
		"台式机，手机, ": {"台式机", "手机"},
		",,":       nil,
	} {
		if got := splitTargets(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitTargets(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"2048": 2048, "512k": 512 << 10, "100MB": 100 << 20, "1.5G": 3 << 29, "1 TB": 1 << 40, "0": 0} {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "MB", "-1", "1PB", "abc"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) accepted", bad)
		}
	}
}

func TestParsePolicy(t *testing.T) {
	for in, want := range map[string]node.ReceivePolicy{
		"auto":  {Mode: node.PolicyAuto},
		"ASK":   {Mode: node.PolicyAsk},
		"100MB": {Mode: node.PolicyAskAbove, Above: 100 << 20},
	} {
		got, err := parsePolicy(in)
		if err != nil || got != want {
			t.Errorf("parsePolicy(%q) = %+v, %v", in, got, err)
		}
	}
	if _, err := parsePolicy("sometimes"); err == nil {
		t.Error("bad policy accepted")
	}
	if got := describePolicy(node.ReceivePolicy{Mode: node.PolicyAskAbove, Above: 100 << 20}); got != "超过 100.0 MB 时询问" {
		t.Errorf("describePolicy = %q", got)
	}
}

func TestDescribeTask(t *testing.T) {
	created := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	next := time.Now().Add(time.Hour)
	for _, c := range []struct {
		task         store.Outgoing
		status, note string
	}{
		{store.Outgoing{Status: store.SendPending, CreatedAt: created}, "排队中", "等对方上线，10-30 09:00 前有效"},
		{store.Outgoing{Status: store.SendInterrupted, Attempts: 2, Error: "连接中断", NextAt: next}, "等待重试", "已重试 2 次：连接中断；下次 " + next.Format("15:04:05")},
		{store.Outgoing{Status: store.SendInterrupted, Attempts: 0, Error: "守护进程已退出"}, "等待重试", "等待续传"},
		{store.Outgoing{Status: store.SendInterrupted, Attempts: 0, Error: "连接中断", NextAt: next}, "等待重试", "等待续传；下次 " + next.Format("15:04:05")},
		{store.Outgoing{Status: store.SendFailed, Error: "对方拒绝了"}, "失败", "对方拒绝了"},
		{store.Outgoing{Status: store.SendDone}, "已发送", ""},
		{store.Outgoing{Status: store.SendPending, CreatedAt: created, Cancel: true}, "取消中", "等对方上线，10-30 09:00 前有效"},
		{store.Outgoing{Status: store.SendInterrupted, Attempts: 2, Error: "连接中断", Cancel: true}, "取消中", "已重试 2 次：连接中断"},
		{store.Outgoing{Status: store.SendActive, Cancel: true}, "取消中", ""},
		{store.Outgoing{Status: store.SendDone, Cancel: true}, "已发送", ""},
		{store.Outgoing{Status: store.SendInterrupted, Paused: true}, "已暂停", "用 portide queue resume 任务号 继续"},
		{store.Outgoing{Status: store.SendDone, Paused: true}, "已发送", ""},
		// 继续之后排队期限从继续的时间算起。
		{store.Outgoing{Status: store.SendPending, CreatedAt: created, ResumedAt: created.Add(24 * time.Hour)}, "排队中", "等对方上线，10-31 09:00 前有效"},
	} {
		status, note := describeTask(c.task, 7)
		if status != c.status || note != c.note {
			t.Errorf("describeTask(%s) = %q, %q; want %q, %q", c.task.Status, status, note, c.status, c.note)
		}
	}
}

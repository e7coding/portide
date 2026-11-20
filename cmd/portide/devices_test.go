package main

import (
	"strings"
	"testing"
)

func TestParseRemoveArgs(t *testing.T) {
	for _, c := range []struct {
		args  []string
		query string
		yes   bool
		force bool
		ok    bool
	}{
		{[]string{"手机"}, "手机", false, false, true},
		{[]string{"--yes", "手机"}, "手机", true, false, true},
		{[]string{"A1B2", "-yes"}, "A1B2", true, false, true},
		{nil, "", false, false, false},
		{[]string{"手机", "平板"}, "", false, false, false},
		{[]string{"手机", "--force"}, "手机", false, true, true},
		{[]string{"--yes", "--force", "手机"}, "手机", true, true, true},
		{[]string{"手机", "-force"}, "手机", false, true, true},
	} {
		q, yes, force, err := parseRemoveArgs(c.args)
		if (err == nil) != c.ok || q != c.query || yes != c.yes || force != c.force {
			t.Errorf("parseRemoveArgs(%q) = %q %v %v %v", c.args, q, yes, force, err)
		}
	}
}

// 测试里标准输入不是终端：没有 --yes 时必须拒绝，而不是悄悄执行。
func TestConfirmNeedsYesWithoutTerminal(t *testing.T) {
	if ok, err := confirm("?", "需要 --yes", false); ok || err == nil || err.Error() != "需要 --yes" {
		t.Fatalf("confirm without a terminal = %v %v", ok, err)
	}
	if ok, err := confirm("?", "需要 --yes", true); !ok || err != nil {
		t.Fatalf("confirm with --yes = %v %v", ok, err)
	}
}

func TestRenameUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"手机"}, {"手机", "新", "多余"}} {
		if err := cmdRename(t.TempDir(), args); err == nil || err.Error() != "用法：portide devices rename 设备名或KeyID前缀 新名字" {
			t.Errorf("cmdRename(%q) = %v, want the usage", args, err)
		}
	}
	if err := cmdRename(t.TempDir(), []string{"手机", " "}); err == nil || !strings.HasPrefix(err.Error(), "新名字不能为空") {
		t.Errorf("blank name: err = %v", err)
	}
}

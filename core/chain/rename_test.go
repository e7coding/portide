package chain

import (
	"strings"
	"testing"
)

// 改名（M3c）：成员可以改自己，也可以改别的成员；签名链重新加载后名字照样；改名不影响成员资格和加入序号。
func TestRenameDevice(t *testing.T) {
	laptop, desktop := mustID(t), mustID(t)
	c, err := New(laptop, "笔记本")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddDevice(laptop, desktop.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameDevice(laptop, laptop.KeyID, "joy 的笔记本"); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameDevice(laptop, desktop.KeyID, "办公室电脑"); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(c.Records())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []*Chain{c, loaded} {
		if m, _ := ch.Member(laptop.KeyID); m.Name != "joy 的笔记本" || m.AddedSeq != 0 {
			t.Fatalf("laptop = %+v", m)
		}
		if m, _ := ch.Member(desktop.KeyID); m.Name != "办公室电脑" || m.AddedSeq != 1 {
			t.Fatalf("desktop = %+v", m)
		}
		if len(ch.Members()) != 2 || ch.Len() != 4 {
			t.Fatalf("members = %d, len = %d", len(ch.Members()), ch.Len())
		}
	}
}

func TestRenameDeviceRules(t *testing.T) {
	laptop, desktop, outsider := mustID(t), mustID(t), mustID(t)
	c, err := New(laptop, "笔记本")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RenameDevice(laptop, desktop.KeyID, "x"); err == nil {
		t.Fatal("renaming a non-member must fail")
	}
	if err := c.RenameDevice(outsider, laptop.KeyID, "x"); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("a non-member signer: %v", err)
	}
	if err := c.RenameDevice(laptop, laptop.KeyID, ""); err == nil {
		t.Fatal("an empty name must fail")
	}
	if err := c.RenameDevice(laptop, laptop.KeyID, strings.Repeat("长", 40)); err == nil {
		t.Fatal("a name over MaxNameBytes must fail")
	}
	if c.Len() != 1 {
		t.Fatalf("failed renames left records: len = %d", c.Len())
	}
	// 被移除的设备不能再被改名。
	if err := c.AddDevice(laptop, desktop.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveDevice(laptop, desktop.KeyID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameDevice(laptop, desktop.KeyID, "x"); err == nil {
		t.Fatal("renaming a removed device must fail")
	}
}

package chain

import (
	"testing"
	"time"
)

// 成员的加入时间取自加入记录（M3c：设备详情显示），重新加载后不变。
func TestMemberAddedAt(t *testing.T) {
	laptop, desktop := mustID(t), mustID(t)
	before := time.Now().Add(-time.Second)
	c, err := New(laptop, "笔记本")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddDevice(laptop, desktop.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(c.Records())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range loaded.Members() {
		if m.AddedAt.Before(before) || m.AddedAt.After(time.Now()) {
			t.Fatalf("%s added at %v", m.Name, m.AddedAt)
		}
	}
}

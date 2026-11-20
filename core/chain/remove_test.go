package chain

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"time"

	"portide/core/pb"
)

func TestRemoveDevice(t *testing.T) {
	laptop, desktop, phone := mustID(t), mustID(t), mustID(t)
	c, _ := New(laptop, "笔记本")
	_ = c.AddDevice(laptop, desktop.Public, "台式机")
	_ = c.AddDevice(desktop, phone.Public, "手机")
	if c.KeyCheck() != nil {
		t.Fatal("a chain without removals must have no key check")
	}

	key := bytes.Repeat([]byte{7}, 32)
	if err := c.RemoveDevice(desktop, phone.KeyID, key); err != nil {
		t.Fatal(err)
	}
	if c.IsMember(phone.KeyID) || len(c.Members()) != 2 || c.Len() != 4 {
		t.Fatalf("after removal: member=%v members=%d len=%d", c.IsMember(phone.KeyID), len(c.Members()), c.Len())
	}
	if !bytes.Equal(c.KeyCheck(), DiscoveryKeyCheck(key)) {
		t.Fatal("key check must be recorded")
	}
	if m, ok := c.Member(laptop.KeyID); !ok || m.Name != "笔记本" {
		t.Fatalf("remaining member lookup broken: %+v %v", m, ok)
	}
	if m, ok := c.FormerMember(phone.KeyID); !ok || m.Name != "手机" {
		t.Fatalf("removed device must still be named: %+v %v", m, ok)
	}

	// 重新加载、合并都要得到同样的结果。
	re, err := Load(c.Records())
	if err != nil {
		t.Fatal(err)
	}
	if re.IsMember(phone.KeyID) || !bytes.Equal(re.KeyCheck(), c.KeyCheck()) {
		t.Fatal("reloaded chain differs")
	}
	old, _ := Load(c.Records()[:3])
	if changed, err := old.Merge(c.Records()); err != nil || !changed || old.IsMember(phone.KeyID) {
		t.Fatalf("merge of a removal: changed=%v err=%v", changed, err)
	}
}

func TestRemovedDeviceLosesSigningRights(t *testing.T) {
	laptop, desktop, phone := mustID(t), mustID(t), mustID(t)
	c, _ := New(laptop, "笔记本")
	_ = c.AddDevice(laptop, desktop.Public, "台式机")
	_ = c.RemoveDevice(laptop, desktop.KeyID, make([]byte, 32))
	if err := c.AddDevice(desktop, phone.Public, "手机"); err == nil {
		t.Fatal("a removed device must not be able to add devices")
	}
	if err := c.RemoveDevice(desktop, laptop.KeyID, make([]byte, 32)); err == nil {
		t.Fatal("a removed device must not be able to remove devices")
	}
}

func TestRemoveRules(t *testing.T) {
	laptop, desktop, outsider := mustID(t), mustID(t), mustID(t)
	c, _ := New(laptop, "笔记本")
	_ = c.AddDevice(laptop, desktop.Public, "台式机")
	if err := c.RemoveDevice(laptop, laptop.KeyID, make([]byte, 32)); err == nil {
		t.Fatal("a device must not be able to remove itself")
	}
	if err := c.RemoveDevice(laptop, outsider.KeyID, make([]byte, 32)); err == nil {
		t.Fatal("removing a non-member must fail")
	}
	if c.Len() != 2 {
		t.Fatalf("failed removals must not append records, len = %d", c.Len())
	}

	// 手工构造的记录：校验值长度不对、目标不是成员，都必须被拒绝。
	prev := sha256.Sum256(c.records[len(c.records)-1])
	bad := []*pb.RemoveDevice{
		{DeviceKey: desktop.Public, DiscoveryKeyCheck: []byte{1, 2, 3}},
		{DeviceKey: outsider.Public, DiscoveryKeyCheck: make([]byte, 32)},
		{DeviceKey: []byte{1}, DiscoveryKeyCheck: make([]byte, 32)},
	}
	for i, op := range bad {
		cp, _ := Load(c.Records())
		body := &pb.RecordBody{Seq: 2, PrevHash: prev[:], TimeUnix: time.Now().Unix(), Op: &pb.RecordBody_RemoveDevice{RemoveDevice: op}}
		if err := cp.appendSigned(laptop, body); err == nil {
			t.Fatalf("bad removal %d must be rejected", i)
		}
	}
}

func TestPrefixFor(t *testing.T) {
	laptop, desktop, phone := mustID(t), mustID(t), mustID(t)
	random := bytes.Repeat([]byte{9}, 32)

	// 没有移除过设备：任何密钥都拿到完整链。
	c, _ := New(laptop, "笔记本")
	_ = c.AddDevice(laptop, desktop.Public, "台式机")
	if got := c.PrefixFor(random); len(got) != c.Len() {
		t.Fatalf("no removals: PrefixFor = %d records, want %d", len(got), c.Len())
	}
	if _, err := Load(c.PrefixFor(random)); err != nil {
		t.Fatalf("prefix must load: %v", err)
	}

	// 一条移除记录，密钥 k1。
	k1 := bytes.Repeat([]byte{1}, 32)
	c, _ = New(laptop, "笔记本")
	_ = c.AddDevice(laptop, desktop.Public, "台式机")
	_ = c.AddDevice(laptop, phone.Public, "手机")
	beforeRemoval := c.Len()
	_ = c.RemoveDevice(laptop, phone.KeyID, k1)
	if got := c.PrefixFor(k1); len(got) != c.Len() {
		t.Fatalf("PrefixFor(k1) = %d, want full chain %d", len(got), c.Len())
	}
	if got := c.PrefixFor(random); len(got) != beforeRemoval {
		t.Fatalf("PrefixFor(random) = %d, want %d (before the removal)", len(got), beforeRemoval)
	}
	k0 := bytes.Repeat([]byte{0}, 32) // 创建时的密钥（本测试没有使用过，等价于"对不上"）
	if got := c.PrefixFor(k0); len(got) != beforeRemoval {
		t.Fatalf("PrefixFor(k0) = %d, want %d (before the removal)", len(got), beforeRemoval)
	}
	if _, err := Load(c.PrefixFor(k1)); err != nil {
		t.Fatalf("prefix for k1 must load: %v", err)
	}
	if _, err := Load(c.PrefixFor(random)); err != nil {
		t.Fatalf("prefix for random must load: %v", err)
	}

	// 两条移除记录，k1 然后 k2，中间插了一次加入设备。
	k2 := bytes.Repeat([]byte{2}, 32)
	c, _ = New(laptop, "笔记本")
	_ = c.AddDevice(laptop, desktop.Public, "台式机")
	_ = c.AddDevice(laptop, phone.Public, "手机")
	_ = c.RemoveDevice(laptop, phone.KeyID, k1)
	tablet := mustID(t)
	_ = c.AddDevice(laptop, tablet.Public, "平板")
	beforeSecondRemoval := c.Len() // 第二条移除记录之前：包含两条移除记录之间插入的加入设备记录
	_ = c.RemoveDevice(laptop, tablet.KeyID, k2)

	if got := c.PrefixFor(k2); len(got) != c.Len() {
		t.Fatalf("PrefixFor(k2) = %d, want full chain %d", len(got), c.Len())
	}
	if got := c.PrefixFor(k1); len(got) != beforeSecondRemoval {
		t.Fatalf("PrefixFor(k1) = %d, want %d (up to, excluding, the second removal)", len(got), beforeSecondRemoval)
	}
	if got := c.PrefixFor(random); len(got) != beforeRemoval {
		t.Fatalf("PrefixFor(random) = %d, want %d (up to the first removal)", len(got), beforeRemoval)
	}
	for _, key := range [][]byte{k1, k2, random} {
		if _, err := Load(c.PrefixFor(key)); err != nil {
			t.Fatalf("prefix for %x must load: %v", key, err)
		}
	}
}

func TestRemovedDeviceCanRejoin(t *testing.T) {
	laptop, desktop := mustID(t), mustID(t)
	c, _ := New(laptop, "笔记本")
	_ = c.AddDevice(laptop, desktop.Public, "台式机")
	_ = c.RemoveDevice(laptop, desktop.KeyID, make([]byte, 32))
	if err := c.AddDevice(laptop, desktop.Public, "台式机"); err != nil {
		t.Fatalf("a removed device must be able to pair again: %v", err)
	}
	if !c.IsMember(desktop.KeyID) {
		t.Fatal("rejoined device must be a member")
	}
	if _, ok := c.FormerMember(desktop.KeyID); ok {
		t.Fatal("a rejoined device is no longer a former member")
	}
	if _, err := Load(c.Records()); err != nil {
		t.Fatal(err)
	}
}

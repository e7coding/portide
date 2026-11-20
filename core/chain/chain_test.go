package chain

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"portide/core/identity"
	"portide/core/pb"
)

func mustID(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNewAndAddDevice(t *testing.T) {
	laptop, desktop := mustID(t), mustID(t)
	c, err := New(laptop, "笔记本")
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsMember(laptop.KeyID) || c.IsMember(desktop.KeyID) {
		t.Fatal("membership after genesis is wrong")
	}
	if err := c.AddDevice(laptop, desktop.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	m, ok := c.Member(desktop.KeyID)
	if !ok || m.Name != "台式机" || m.AddedSeq != 1 {
		t.Fatalf("desktop member = %+v %v", m, ok)
	}
	if got := len(c.Members()); got != 2 {
		t.Fatalf("members = %d, want 2", got)
	}

	// 重新加载必须得到同样的圈子和成员。
	re, err := Load(c.Records())
	if err != nil {
		t.Fatal(err)
	}
	if re.CircleID() != c.CircleID() || !re.IsMember(desktop.KeyID) {
		t.Fatal("reloaded chain differs")
	}
}

func TestNonMemberCannotSign(t *testing.T) {
	laptop, outsider, victim := mustID(t), mustID(t), mustID(t)
	c, _ := New(laptop, "笔记本")
	if err := c.AddDevice(outsider, victim.Public, "x"); err == nil {
		t.Fatal("a non-member must not be able to add devices")
	}
}

func TestDuplicateMemberRejected(t *testing.T) {
	laptop, desktop := mustID(t), mustID(t)
	c, _ := New(laptop, "笔记本")
	_ = c.AddDevice(laptop, desktop.Public, "台式机")
	if err := c.AddDevice(laptop, desktop.Public, "台式机"); err == nil {
		t.Fatal("adding the same device twice must fail")
	}
}

func TestLoadRejectsTampering(t *testing.T) {
	laptop, desktop := mustID(t), mustID(t)
	c, _ := New(laptop, "笔记本")
	_ = c.AddDevice(laptop, desktop.Public, "台式机")
	recs := c.Records()

	// 篡改第二条记录的设备名：签名失效。
	var rec pb.Record
	_ = proto.Unmarshal(recs[1], &rec)
	var body pb.RecordBody
	_ = proto.Unmarshal(rec.Body, &body)
	body.GetAddDevice().DeviceName = "evil"
	rec.Body, _ = proto.Marshal(&body)
	recs[1], _ = proto.Marshal(&rec)
	if _, err := Load(recs); err == nil {
		t.Fatal("tampered chain must not load")
	}

	// 删除中间记录：seq / prev_hash 不连续。
	other, _ := New(laptop, "笔记本")
	_ = other.AddDevice(laptop, desktop.Public, "台式机")
	third := mustID(t)
	_ = other.AddDevice(laptop, third.Public, "平板")
	r := other.Records()
	if _, err := Load([][]byte{r[0], r[2]}); err == nil {
		t.Fatal("chain with a gap must not load")
	}
}

func TestDeviceNameRule(t *testing.T) {
	laptop, desktop := mustID(t), mustID(t)

	longName := strings.Repeat("x", MaxNameBytes+1)
	if _, err := New(laptop, longName); err == nil {
		t.Fatal("New with a name longer than MaxNameBytes must fail")
	}

	c, err := New(laptop, "笔记本")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddDevice(laptop, desktop.Public, "\x1b[31mevil"); err == nil {
		t.Fatal("AddDevice with control characters in the name must fail")
	}
	if err := c.AddDevice(laptop, desktop.Public, ""); err == nil {
		t.Fatal("AddDevice with an empty name must fail")
	}
	if err := c.AddDevice(laptop, desktop.Public, "台式机-Windows 笔记本"); err != nil {
		t.Fatalf("AddDevice with a valid Chinese/English name must succeed: %v", err)
	}
}

func TestMerge(t *testing.T) {
	laptop, desktop, tablet := mustID(t), mustID(t), mustID(t)
	a, _ := New(laptop, "笔记本")
	b, _ := Load(a.Records())

	// a 延伸 → b 采纳。
	_ = a.AddDevice(laptop, desktop.Public, "台式机")
	changed, err := b.Merge(a.Records())
	if err != nil || !changed || !b.IsMember(desktop.KeyID) {
		t.Fatalf("merge of a longer chain: changed=%v err=%v", changed, err)
	}
	// 相同或更短 → 不变。
	if changed, err := a.Merge(b.Records()); err != nil || changed {
		t.Fatalf("merge of an equal chain: changed=%v err=%v", changed, err)
	}
	// 两边各自追加不同记录 → 分叉。
	_ = a.AddDevice(laptop, tablet.Public, "平板")
	_ = b.AddDevice(desktop, mustID(t).Public, "手机")
	if _, err := a.Merge(b.Records()); !errors.Is(err, ErrFork) {
		t.Fatalf("diverged chains: err = %v, want ErrFork", err)
	}
	// 另一个圈子。
	other, _ := New(mustID(t), "别人")
	if _, err := a.Merge(other.Records()); !errors.Is(err, ErrOtherCircle) {
		t.Fatalf("other circle: err = %v, want ErrOtherCircle", err)
	}
}

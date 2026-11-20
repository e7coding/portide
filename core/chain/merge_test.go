package chain

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"portide/core/identity"
)

// forkPair 返回同一条基础链的两份副本：笔记本创建，台式机加入。
func forkPair(t *testing.T) (laptop, desktop *identity.Identity, a, b *Chain) {
	t.Helper()
	laptop, desktop = mustID(t), mustID(t)
	base, err := New(laptop, "笔记本")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.AddDevice(laptop, desktop.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	a, err = Load(base.Records())
	if err != nil {
		t.Fatal(err)
	}
	b, err = Load(base.Records())
	if err != nil {
		t.Fatal(err)
	}
	return laptop, desktop, a, b
}

// exchange 模拟两台设备连上后的同步：先各自合并对方握手时出示的链，变了的一方再把新链推给对方。
func exchange(t *testing.T, ida *identity.Identity, a *Chain, idb *identity.Identity, b *Chain) {
	t.Helper()
	helloA, helloB := a.Records(), b.Records()
	chA, _, err := a.MergeAs(ida, helloB, nil)
	if err != nil {
		t.Fatalf("a merges b: %v", err)
	}
	chB, _, err := b.MergeAs(idb, helloA, nil)
	if err != nil {
		t.Fatalf("b merges a: %v", err)
	}
	if chA {
		if _, _, err := b.MergeAs(idb, a.Records(), nil); err != nil {
			t.Fatalf("b merges the push from a: %v", err)
		}
	}
	if chB {
		if _, _, err := a.MergeAs(ida, b.Records(), nil); err != nil {
			t.Fatalf("a merges the push from b: %v", err)
		}
	}
	if !slices.EqualFunc(a.Records(), b.Records(), func(x, y []byte) bool { return string(x) == string(y) }) {
		t.Fatalf("chains did not converge: len %d vs %d", a.Len(), b.Len())
	}
}

func names(c *Chain) []string {
	var out []string
	for _, m := range c.Members() {
		out = append(out, m.Name)
	}
	return out
}

func TestMergeAsResolvesRenameForks(t *testing.T) {
	// 两台设备离线时各改了自己的名字：连上后两个改名都保留。
	laptop, desktop, a, b := forkPair(t)
	if err := a.RenameDevice(laptop, laptop.KeyID, "joy 的笔记本"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, desktop.KeyID, "办公室电脑"); err != nil {
		t.Fatal(err)
	}
	exchange(t, laptop, a, desktop, b)
	if got := names(a); !slices.Equal(got, []string{"joy 的笔记本", "办公室电脑"}) {
		t.Fatalf("names = %q", got)
	}
	if _, err := Load(a.Records()); err != nil {
		t.Fatalf("merged chain does not load: %v", err)
	}

	// 两边把同一台设备改成了不同的名字：结果确定，两边一致。
	laptop, desktop, a, b = forkPair(t)
	if err := a.RenameDevice(laptop, desktop.KeyID, "甲"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, desktop.KeyID, "乙"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, laptop.KeyID, "丙"); err != nil {
		t.Fatal(err)
	}
	exchange(t, laptop, a, desktop, b)
	if got := names(a); got[0] != "丙" || (got[1] != "甲" && got[1] != "乙") {
		t.Fatalf("names = %q", got)
	}

	// 败方的改名已经和胜方一致：直接采用胜方的链，不再追加。
	laptop, desktop, a, b = forkPair(t)
	if err := a.RenameDevice(laptop, desktop.KeyID, "同名"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, desktop.KeyID, "同名"); err != nil {
		t.Fatal(err)
	}
	exchange(t, laptop, a, desktop, b)
	if a.Len() != 3 {
		t.Fatalf("len = %d, want 3 (no extra rename)", a.Len())
	}

	// 败方改过名又改回原名：净改动为空，也不追加。
	laptop, desktop, a, b = forkPair(t)
	if err := a.RenameDevice(laptop, laptop.KeyID, "临时"); err != nil {
		t.Fatal(err)
	}
	if err := a.RenameDevice(laptop, laptop.KeyID, "笔记本"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, desktop.KeyID, "办公室电脑"); err != nil {
		t.Fatal(err)
	}
	exchange(t, laptop, a, desktop, b)
	if got := names(a); !slices.Equal(got, []string{"笔记本", "办公室电脑"}) {
		t.Fatalf("names = %q", got)
	}
}

// 三台设备离线时把同一台设备改成三个不同的名字：无论按什么顺序两两同步，最后三条链逐字节相同。
func TestMergeAsThreeWayConflictingRenames(t *testing.T) {
	laptop, desktop, a, b := forkPair(t)
	phone := mustID(t)
	for _, ch := range []*Chain{a, b} {
		if err := ch.AddDevice(laptop, phone.Public, "手机"); err != nil {
			t.Fatal(err)
		}
	}
	c, err := Load(a.Records())
	if err != nil {
		t.Fatal(err)
	}
	ids := []*identity.Identity{laptop, desktop, phone}
	chains := []*Chain{a, b, c}
	for i, name := range []string{"甲", "乙", "丙"} {
		if err := chains[i].RenameDevice(ids[i], desktop.KeyID, name); err != nil {
			t.Fatal(err)
		}
	}
	// 两个输家各自重签后会再分叉一次，多同步几轮也必须收敛。
	for round := 0; round < 3; round++ {
		exchange(t, ids[0], chains[0], ids[1], chains[1])
		exchange(t, ids[1], chains[1], ids[2], chains[2])
		exchange(t, ids[2], chains[2], ids[0], chains[0])
	}
	first := chains[0].Records()
	for _, ch := range chains[1:] {
		if !slices.EqualFunc(ch.Records(), first, func(x, y []byte) bool { return string(x) == string(y) }) {
			t.Fatal("chains differ after syncing")
		}
	}
	if m, _ := chains[0].Member(desktop.KeyID); m.Name != "甲" && m.Name != "乙" && m.Name != "丙" {
		t.Fatalf("name = %q", m.Name)
	}
	if n := chains[0].Len(); n > 3+3+2 {
		t.Fatalf("len = %d: re-signing kept growing the chain", n)
	}
}

func TestMergeAsRebasesRenamesOntoMembershipChanges(t *testing.T) {
	// 一边加了新设备，另一边只改了名：加入记录那边胜出，改名接在后面。
	laptop, desktop, a, b := forkPair(t)
	phone := mustID(t)
	if err := a.AddDevice(laptop, phone.Public, "手机"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, desktop.KeyID, "办公室电脑"); err != nil {
		t.Fatal(err)
	}
	exchange(t, laptop, a, desktop, b)
	if got := names(b); !slices.Equal(got, []string{"笔记本", "办公室电脑", "手机"}) {
		t.Fatalf("names = %q", got)
	}

	// 一边移除了台式机，另一边给台式机改了名：移除那边胜出，给已移除设备的改名丢掉。
	laptop, desktop, a, b = forkPair(t)
	phone = mustID(t)
	for _, c := range []*Chain{a, b} {
		if err := c.AddDevice(laptop, phone.Public, "手机"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.RemoveDevice(laptop, desktop.KeyID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(phone, desktop.KeyID, "旧电脑"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(phone, phone.KeyID, "旧手机"); err != nil {
		t.Fatal(err)
	}
	exchange(t, laptop, a, phone, b)
	if got := names(b); !slices.Equal(got, []string{"笔记本", "旧手机"}) {
		t.Fatalf("names = %q", got)
	}

	// 被移除的设备自己改了名：它不再是成员，只能采用对方的链，不追加任何记录。
	laptop, desktop, a, b = forkPair(t)
	if err := a.RemoveDevice(laptop, desktop.KeyID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, desktop.KeyID, "办公室电脑"); err != nil {
		t.Fatal(err)
	}
	if ch, _, err := b.MergeAs(desktop, a.Records(), nil); err != nil || !ch || b.Len() != a.Len() || b.IsMember(desktop.KeyID) {
		t.Fatalf("removed device: changed=%v err=%v len=%d", ch, err, b.Len())
	}
}

func TestMergeAsKeepsOtherForks(t *testing.T) {
	// 两边都有加入或移除记录时不自动处理，仍然返回 ErrFork，两条链都不变。
	laptop, desktop, a, b := forkPair(t)
	if err := a.AddDevice(laptop, mustID(t).Public, "手机"); err != nil {
		t.Fatal(err)
	}
	if err := b.AddDevice(desktop, mustID(t).Public, "平板"); err != nil {
		t.Fatal(err)
	}
	lenA, lenB := a.Len(), b.Len()
	if _, _, err := a.MergeAs(laptop, b.Records(), nil); !errors.Is(err, ErrFork) {
		t.Fatalf("a: err = %v, want ErrFork", err)
	}
	if _, _, err := b.MergeAs(desktop, a.Records(), nil); !errors.Is(err, ErrFork) {
		t.Fatalf("b: err = %v, want ErrFork", err)
	}
	if a.Len() != lenA || b.Len() != lenB {
		t.Fatal("a failed merge changed a chain")
	}

	// 其他情况和 Merge 一样：延伸则采纳，另一个设备圈报 ErrOtherCircle。
	laptop, _, a, b = forkPair(t)
	if err := b.RenameDevice(laptop, laptop.KeyID, "新名字"); err != nil {
		t.Fatal(err)
	}
	if ch, _, err := a.MergeAs(laptop, b.Records(), nil); err != nil || !ch || a.Len() != b.Len() {
		t.Fatalf("extension: changed=%v err=%v", ch, err)
	}
	if ch, _, err := b.MergeAs(laptop, forkRecords(t), nil); !errors.Is(err, ErrOtherCircle) || ch {
		t.Fatalf("other circle: changed=%v err=%v", ch, err)
	}
}

func forkRecords(t *testing.T) [][]byte {
	t.Helper()
	c, err := New(mustID(t), "别人的")
	if err != nil {
		t.Fatal(err)
	}
	return c.Records()
}

func TestRenameOnly(t *testing.T) {
	laptop, desktop, a, _ := forkPair(t)
	if a.RenameOnlySince(1) || !a.RenameOnlySince(2) {
		t.Fatal("the add record is not a rename")
	}
	if err := a.RenameDevice(laptop, desktop.KeyID, "x"); err != nil {
		t.Fatal(err)
	}
	if !a.RenameOnlySince(2) || !a.HasRenames() {
		t.Fatal("a rename suffix")
	}
	_, _, b, _ := forkPair(t)
	if b.HasRenames() {
		t.Fatal("no renames yet")
	}
}

// 胜出的链里有成员不支持改名时（例如刚配对了一台旧版本设备），输的一方不补签自己的改名：
// 直接采用胜出的链，并报告没补签的名字，免得那台旧设备从此读不了链。
func TestMergeAsSkipsResignWhenNotAllowed(t *testing.T) {
	laptop, desktop, a, b := forkPair(t)
	old := mustID(t)
	if err := a.AddDevice(laptop, old.Public, "旧手机"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, desktop.KeyID, "办公室电脑"); err != nil {
		t.Fatal(err)
	}
	var asked *Chain
	ch, skipped, err := b.MergeAs(desktop, a.Records(), func(winner *Chain) bool {
		asked = winner
		return false
	})
	if err != nil || !ch {
		t.Fatalf("changed=%v err=%v", ch, err)
	}
	if asked == nil || !asked.IsMember(old.KeyID) {
		t.Fatal("the policy must be asked about the winning chain")
	}
	if !slices.Equal(skipped, []string{"办公室电脑"}) {
		t.Fatalf("skipped = %q", skipped)
	}
	if b.Len() != a.Len() || b.HasRenames() {
		t.Fatalf("b must equal a without re-signed renames: len %d vs %d", b.Len(), a.Len())
	}

	// 允许时照常补签，没有跳过的名字。
	laptop, desktop, a, b = forkPair(t)
	if err := a.AddDevice(laptop, mustID(t).Public, "手机"); err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, desktop.KeyID, "办公室电脑"); err != nil {
		t.Fatal(err)
	}
	ch, skipped, err = b.MergeAs(desktop, a.Records(), func(*Chain) bool { return true })
	if err != nil || !ch || len(skipped) != 0 || b.Len() != a.Len()+1 {
		t.Fatalf("changed=%v skipped=%q err=%v len=%d", ch, skipped, err, b.Len())
	}
}

// 整个设备圈的改名记录有上限：到了上限不能再改名，自动收敛时也不再补签。
func TestRenameLimit(t *testing.T) {
	laptop, desktop, a, _ := forkPair(t)
	// 先加一台设备：后面对方只有改名时，有加入记录的这条链一定胜出。
	if err := a.AddDevice(laptop, mustID(t).Public, "手机"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxRenames; i++ {
		if err := a.RenameDevice(laptop, desktop.KeyID, fmt.Sprintf("台式机 %d", i)); err != nil {
			t.Fatalf("rename %d: %v", i, err)
		}
	}
	if a.RenameCount() != MaxRenames {
		t.Fatalf("RenameCount = %d", a.RenameCount())
	}
	before := a.Len()
	if err := a.RenameDevice(laptop, desktop.KeyID, "再改一次"); !errors.Is(err, ErrTooManyRenames) {
		t.Fatalf("over the limit: err = %v, want ErrTooManyRenames", err)
	}
	if a.Len() != before {
		t.Fatal("a refused rename must not append")
	}
	// 已经有这么多改名记录的链照样能加载（只限制新建，不拒绝已有的链）。
	if _, err := Load(a.Records()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 对方的链已到上限：输的一方采用它，但补签不了，报告跳过的名字而不是失败。
	b, err := Load(a.Records()[:2])
	if err != nil {
		t.Fatal(err)
	}
	if err := b.RenameDevice(desktop, desktop.KeyID, "办公室电脑"); err != nil {
		t.Fatal(err)
	}
	ch, skipped, err := b.MergeAs(desktop, a.Records(), nil)
	if err != nil || !ch || !slices.Equal(skipped, []string{"办公室电脑"}) || b.Len() != a.Len() {
		t.Fatalf("changed=%v skipped=%q err=%v len=%d/%d", ch, skipped, err, b.Len(), a.Len())
	}
}

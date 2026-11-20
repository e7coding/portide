package node

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"portide/core/chain"
	"portide/core/identity"
	"portide/core/store"
)

// 还有成员没声明支持改名（这里是从没连上过的手机）时不能改名；声明之后可以，本机名跟着链走。
func TestRenameNeedsEveryMemberToSupportIt(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	phone := withExtraMember(t, laptop, "手机")

	ok, missing, err := laptop.n.CanRename()
	if err != nil || ok || !slices.Equal(missing, []string{"手机"}) {
		t.Fatalf("CanRename = %v %v %v, want false [手机]", ok, missing, err)
	}
	if err := laptop.n.RenameDevice(laptop.n.id.KeyID, "新笔记本"); !errors.Is(err, ErrRenameUnsupported) {
		t.Fatalf("rename: err = %v, want ErrRenameUnsupported", err)
	}

	_ = laptop.n.store.SetPeerFeatures(phone.KeyID, []string{"other", FeatureRename})
	if ok, _, _ := laptop.n.CanRename(); !ok {
		t.Fatal("every other member declared rename, CanRename must be true")
	}
	if err := laptop.n.RenameDevice(laptop.n.id.KeyID, "新笔记本"); err != nil {
		t.Fatal(err)
	}
	if got := laptop.n.Name(); got != "新笔记本" {
		t.Fatalf("Name() = %q, want the renamed name", got)
	}
	if err := laptop.n.RenameDevice(phone.KeyID, ""); err == nil {
		t.Fatal("an empty name must be rejected")
	}
}

// 握手时互相记下对方的功能；在笔记本上给手机改名后，台式机和手机自己都拿到新名字。
func TestRenameSpreadsToMembers(t *testing.T) {
	laptop, desktop, phone := circleOfThree(t, nil)
	desktop.start(t)
	phone.start(t)
	eventually(t, "all online", func() bool {
		return online(t, laptop, desktop.n.id.KeyID) && online(t, laptop, phone.n.id.KeyID)
	})
	eventually(t, "features recorded", func() bool { ok, _, _ := laptop.n.CanRename(); return ok })

	if err := laptop.n.RenameDevice(phone.n.id.KeyID, "旧手机"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone learns its new name", func() bool { return phone.n.Name() == "旧手机" })
	eventually(t, "desktop sees the new name", func() bool {
		c, _ := desktop.n.snapshot()
		m, ok := c.Member(phone.n.id.KeyID)
		return ok && m.Name == "旧手机"
	})
}

// 三台设备互相没连上时各改了自己的名字（签名链三方分叉）：连上之后自动收敛到同一条链，三个新名字都在，并且能保持连接。
func TestOfflineRenamesConverge(t *testing.T) {
	laptop, desktop, phone := circleOfThree(t, nil)
	all := []*testDevice{laptop, desktop, phone}
	for _, d := range all {
		for _, o := range all {
			if o != d {
				_ = d.n.store.SetPeerFeatures(o.n.id.KeyID, []string{FeatureRename})
			}
		}
	}
	want := map[*testDevice]string{laptop: "joy 的笔记本", desktop: "办公室电脑", phone: "旧手机"}
	for _, d := range all {
		if err := d.n.RenameDevice(d.n.id.KeyID, want[d]); err != nil {
			t.Fatal(err)
		}
	}
	desktop.start(t)
	phone.start(t)
	eventually(t, "chains converge with every new name", func() bool {
		ref, _ := laptop.n.snapshot()
		for _, d := range all {
			c, _ := d.n.snapshot()
			if !slices.EqualFunc(c.Records(), ref.Records(), bytes.Equal) {
				return false
			}
			for _, o := range all {
				if m, ok := c.Member(o.n.id.KeyID); !ok || m.Name != want[o] {
					return false
				}
			}
		}
		return true
	})
	eventually(t, "all online", func() bool {
		return online(t, laptop, desktop.n.id.KeyID) && online(t, laptop, phone.n.id.KeyID)
	})
	for _, d := range all {
		if got := d.n.Name(); got != want[d] {
			t.Fatalf("Name() = %q, want %q", got, want[d])
		}
		recs, err := d.n.store.LoadChain()
		if err != nil {
			t.Fatal(err)
		}
		c, _ := d.n.snapshot()
		if !slices.EqualFunc(recs, c.Records(), bytes.Equal) {
			t.Fatalf("%s: stored chain differs from the one in memory", want[d])
		}
	}
}

// 改成现在的名字不追加记录（改名记录永久留在链上，每次握手都要传）。
func TestRenameToTheSameNameIsANoop(t *testing.T) {
	laptop, _, _ := circleOfThree(t, nil)
	before, _ := laptop.n.snapshot()
	if err := laptop.n.RenameDevice(laptop.n.id.KeyID, "笔记本"); err != nil {
		t.Fatal(err)
	}
	if after, _ := laptop.n.snapshot(); after.Len() != before.Len() {
		t.Fatalf("len = %d, want %d", after.Len(), before.Len())
	}
}

func TestPairFailureText(t *testing.T) {
	for reason, want := range map[string]string{
		store.PairErrOutdated:        "对方的 Portide 版本太旧，请先在那台设备上升级，再重新添加",
		store.PairErrTooManyAttempts: "配对码输错的次数太多，已经作废，请重新生成",
		store.PairErrCancelled:       "这次配对已经取消",
		"其他":                         "其他",
	} {
		if got := PairFailureText(reason); got != want {
			t.Errorf("PairFailureText(%q) = %q, want %q", reason, got, want)
		}
	}
}

// 数据库里的链被换掉了分歧的后缀（可能比内存里的短）：重新加载时照样采用它，否则内存和数据库会一直对不上。
func TestReloadAdoptsAReplacedShorterChain(t *testing.T) {
	laptop, desktop, _ := circleOfThree(t, nil)
	c, _ := laptop.n.snapshot()
	old := c.Records()
	next, err := chain.Load(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.RenameDevice(laptop.n.id, desktop.n.id.KeyID, "甲"); err != nil {
		t.Fatal(err)
	}
	if err := next.RenameDevice(laptop.n.id, desktop.n.id.KeyID, "乙"); err != nil {
		t.Fatal(err)
	}
	laptop.n.mu.Lock()
	err = laptop.n.store.AppendChain(len(old), next.Records())
	if err == nil {
		err = laptop.n.useChainLocked(next)
	}
	laptop.n.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// 另一份更短、内容不同的链直接写进数据库，模拟替换成功、内存却没跟上。
	other, err := chain.Load(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.RenameDevice(laptop.n.id, desktop.n.id.KeyID, "丙"); err != nil {
		t.Fatal(err)
	}
	if err := laptop.n.store.ReplaceChain(next.Records(), other.Records()); err != nil {
		t.Fatal(err)
	}
	if changed, err := laptop.n.reloadCircle(); err != nil || !changed {
		t.Fatalf("reloadCircle = %v %v, want true", changed, err)
	}
	got, _ := laptop.n.snapshot()
	if m, _ := got.Member(desktop.n.id.KeyID); m.Name != "丙" || got.Len() != other.Len() {
		t.Fatalf("memory chain: name %q len %d", m.Name, got.Len())
	}
	// 数据库是内存链的前缀（读库和合并新链并发时的旧读数）：不回退。
	if err := laptop.n.store.ReplaceChain(other.Records(), old); err != nil {
		t.Fatal(err)
	}
	if changed, _ := laptop.n.reloadCircle(); changed {
		t.Fatal("a stored prefix of the memory chain must not replace it")
	}
}

// 别的设备给本机改了名：本机连上、合并新链之后，记在数据库里的设备名也跟着变（以后被移除再重新配对时用的是它）。
func TestOwnNameFollowsTheChain(t *testing.T) {
	laptop, desktop, phone := circleOfThree(t, nil)
	for _, d := range []*testDevice{desktop, phone} {
		_ = laptop.n.store.SetPeerFeatures(d.n.id.KeyID, []string{FeatureRename})
	}
	if err := laptop.n.RenameDevice(desktop.n.id.KeyID, "办公室电脑"); err != nil {
		t.Fatal(err)
	}
	desktop.start(t)
	eventually(t, "desktop stores its new name", func() bool {
		v, _, _ := desktop.n.store.GetMeta(store.MetaDeviceName)
		return v == "办公室电脑" && desktop.n.Name() == "办公室电脑"
	})
}

// 自动收敛时能不能补签：胜出链上其他成员都声明过支持改名才行；从没连上过的设备也算不支持。
func TestResignNeedsEveryOtherMemberToSupportRename(t *testing.T) {
	laptop, desktop, phone := circleOfThree(t, nil)
	c, _ := laptop.n.snapshot()
	_ = laptop.n.store.SetPeerFeatures(desktop.n.id.KeyID, []string{FeatureRename})
	missing, err := laptop.n.renameUnsupported(c)
	if err != nil || !slices.Equal(missing, []string{"手机"}) {
		t.Fatalf("renameUnsupported = %v %v, want [手机]", missing, err)
	}
	_ = laptop.n.store.SetPeerFeatures(phone.n.id.KeyID, []string{"other", FeatureRename})
	if missing, err := laptop.n.renameUnsupported(c); err != nil || len(missing) != 0 {
		t.Fatalf("renameUnsupported = %v %v, want none", missing, err)
	}
}

// 合并时的补签策略接在节点上：胜出的链里有本机没握过手的成员、而且链上还没有改名记录时（它可能是旧版本），
// 本机的改名不补签；胜出的链已经有改名记录时（旧版本早已读不了，配对也会拒绝旧版本），照样补签。
func TestMergeResignPolicyOnTheNode(t *testing.T) {
	for _, winnerHasRenames := range []bool{false, true} {
		laptop, desktop, phone := circleOfThree(t, nil)
		// 台式机离线时给自己改名（它只知道笔记本支持改名）。
		_ = desktop.n.store.SetPeerFeatures(laptop.n.id.KeyID, []string{FeatureRename})
		_ = desktop.n.store.SetPeerFeatures(phone.n.id.KeyID, []string{FeatureRename})
		if err := desktop.n.RenameDevice(desktop.n.id.KeyID, "办公室电脑"); err != nil {
			t.Fatal(err)
		}
		// 笔记本那边同时配对了一台台式机从没见过的新设备（可能还改了自己的名字）。
		lc, _ := laptop.n.snapshot()
		winner, err := chain.Load(lc.Records())
		if err != nil {
			t.Fatal(err)
		}
		if winnerHasRenames {
			if err := winner.RenameDevice(laptop.n.id, laptop.n.id.KeyID, "joy 的笔记本"); err != nil {
				t.Fatal(err)
			}
		}
		stranger, err := identity.Generate()
		if err != nil {
			t.Fatal(err)
		}
		if err := winner.AddDevice(laptop.n.id, stranger.Public, "新平板"); err != nil {
			t.Fatal(err)
		}
		if _, err := desktop.n.mergeChain(winner.Records()); err != nil {
			t.Fatal(err)
		}
		got := desktop.n.Name()
		if winnerHasRenames && got != "办公室电脑" {
			t.Fatalf("winner has renames: name = %q, want the re-signed 办公室电脑", got)
		}
		if !winnerHasRenames && got != "台式机" {
			t.Fatalf("winner has no renames and an unknown member: name = %q, want 台式机 (not re-signed)", got)
		}
	}
}

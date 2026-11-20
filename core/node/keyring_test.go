package node

import (
	"bytes"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"portide/core/chain"
	"portide/core/identity"
	"portide/core/keystore"
)

// withExtraMember 给 d 的设备圈加一个只存在于签名链里的成员，返回它的身份。
func withExtraMember(t *testing.T, d *testDevice, name string) *identity.Identity {
	t.Helper()
	if err := d.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	other, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	c, _ := d.n.snapshot()
	next, _ := chain.Load(c.Records())
	if err := next.AddDevice(d.n.id, other.Public, name); err != nil {
		t.Fatal(err)
	}
	if _, err := d.n.mergeChain(next.Records()); err != nil {
		t.Fatal(err)
	}
	return other
}

func TestRemoveDeviceRotatesDiscoveryKey(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	phone := withExtraMember(t, laptop, "手机")
	_, oldKey := laptop.n.snapshot()

	if err := laptop.n.RemoveDevice(laptop.n.id.KeyID); !errors.Is(err, ErrRemoveSelf) {
		t.Fatalf("removing self: err = %v, want ErrRemoveSelf", err)
	}
	if err := laptop.n.RemoveDevice(phone.KeyID); err != nil {
		t.Fatal(err)
	}
	c, key := laptop.n.snapshot()
	if c.IsMember(phone.KeyID) {
		t.Fatal("phone must no longer be a member")
	}
	if bytes.Equal(key, oldKey) || !bytes.Equal(chain.DiscoveryKeyCheck(key), c.KeyCheck()) {
		t.Fatal("discovery key must rotate to the key recorded in the removal")
	}
	if got := laptop.n.circleKeys(); len(got) != 2 || !bytes.Equal(got[0], key) || !bytes.Equal(got[1], oldKey) {
		t.Fatalf("accepted keys = %d, want the new key then the old one", len(got))
	}
	if stored, _ := laptop.keys.Get(keystore.DiscoveryKey); !bytes.Equal(stored, key) {
		t.Fatal("the new key must be the stored current key")
	}

	// 重新打开（例如命令行和守护进程各自打开）得到同样的密钥。
	again, err := Open(Options{DataDir: laptop.dir, Keys: laptop.keys, DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, k := again.snapshot(); !bytes.Equal(k, key) || len(again.circleKeys()) != 2 {
		t.Fatal("reopened node must use the rotated key and still accept the old one")
	}
}

// 新密钥已经存进密钥库、移除记录也写进了数据库，但还没来得及启用（进程在中途退出）：下次打开时补上。
func TestOpenFinishesInterruptedRotation(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	phone := withExtraMember(t, laptop, "手机")
	c, oldKey := laptop.n.snapshot()
	next, _ := chain.Load(c.Records())
	key, _ := newDiscoveryKey()
	if err := next.RemoveDevice(laptop.n.id, phone.KeyID, key); err != nil {
		t.Fatal(err)
	}
	if err := stageDiscoveryKey(laptop.keys, laptop.n.Store().WithWriteLock, key); err != nil {
		t.Fatal(err)
	}
	if err := laptop.n.Store().AppendChain(c.Len(), next.Records()); err != nil {
		t.Fatal(err)
	}

	again, err := Open(Options{DataDir: laptop.dir, Keys: laptop.keys, DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, k := again.snapshot(); !bytes.Equal(k, key) {
		t.Fatal("open must activate the staged key named by the removal record")
	}
	ring, _ := loadRing(laptop.keys)
	if len(ring) != 1 || !bytes.Equal(ring[0].Key, oldKey) || ring[0].Retired == 0 {
		t.Fatalf("ring = %+v, want only the retired old key", ring)
	}
}

// 移除时不在线的成员拿不到新密钥：继续用原来的密钥，等成员下发。
func TestLaggingMemberKeepsItsKey(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	phone := withExtraMember(t, laptop, "手机")
	c, oldKey := laptop.n.snapshot()
	next, _ := chain.Load(c.Records())
	newKey, _ := newDiscoveryKey()
	_ = next.RemoveDevice(laptop.n.id, phone.KeyID, newKey) // 别的成员做的移除：本机没有 newKey
	if _, err := laptop.n.mergeChain(next.Records()); err != nil {
		t.Fatal(err)
	}
	if _, k := laptop.n.snapshot(); !bytes.Equal(k, oldKey) {
		t.Fatal("a member without the new key must keep using its current key")
	}
}

func TestRetiredKeysExpire(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	phone := withExtraMember(t, laptop, "手机")
	if err := laptop.n.RemoveDevice(phone.KeyID); err != nil {
		t.Fatal(err)
	}
	c, key := laptop.n.snapshot()
	set, err := resolveDiscoveryKeys(laptop.keys, laptop.n.Store().WithWriteLock, c, time.Now().Add(retiredKeyGrace+time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := set.accepted(time.Now().Add(retiredKeyGrace + time.Hour)); len(got) != 1 || !bytes.Equal(got[0], key) {
		t.Fatalf("after the grace period only the current key is accepted, got %d", len(got))
	}
	if ring, _ := loadRing(laptop.keys); len(ring) != 0 {
		t.Fatalf("expired keys must be dropped from the ring, got %d", len(ring))
	}
}

// 一把已经 stage 但从来没有被启用的密钥（例如这次移除从未被别的成员采纳、后来被别的移除覆盖）：
// 超过 staleStagedGrace 还没启用就从密钥环里删掉，不能永久占着密钥库的空间。
func TestStaleStagedKeysAreDropped(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	if err := laptop.n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	stale, _ := newDiscoveryKey()
	if err := stageDiscoveryKey(laptop.keys, laptop.n.Store().WithWriteLock, stale); err != nil {
		t.Fatal(err)
	}
	if ring, _ := loadRing(laptop.keys); len(ring) != 1 {
		t.Fatalf("ring = %+v, want the staged key", ring)
	}
	c, _ := laptop.n.snapshot()
	if _, err := resolveDiscoveryKeys(laptop.keys, laptop.n.Store().WithWriteLock, c, time.Now().Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ring, _ := loadRing(laptop.keys); len(ring) != 0 {
		t.Fatalf("stale staged key must be dropped, ring = %+v", ring)
	}
}

// 命令行刚追加了移除记录，守护进程内存里还是旧链时合并对方的更新：不能把移除记录覆盖掉。
func TestMergeKeepsConcurrentRemoval(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	phone := withExtraMember(t, laptop, "手机")
	c, _ := laptop.n.snapshot()

	cli, err := Open(Options{DataDir: laptop.dir, Keys: laptop.keys, DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if err := cli.RemoveDevice(phone.KeyID); err != nil {
		t.Fatal(err)
	}

	// 对方在旧链上加了一台设备：与移除记录分叉。
	peer, _ := chain.Load(c.Records())
	tablet, _ := identity.Generate()
	_ = peer.AddDevice(laptop.n.id, tablet.Public, "平板")
	if _, err := laptop.n.mergeChain(peer.Records()); !errors.Is(err, chain.ErrFork) {
		t.Fatalf("merge over a concurrent removal: err = %v, want ErrFork", err)
	}
	recs, _ := laptop.n.Store().LoadChain()
	stored, err := chain.Load(recs)
	if err != nil || stored.IsMember(phone.KeyID) || stored.IsMember(tablet.KeyID) {
		t.Fatal("the stored chain must still hold the removal")
	}
	if now, _ := laptop.n.snapshot(); now.IsMember(phone.KeyID) {
		t.Fatal("the daemon must pick up the removal from the database")
	}
}

// TestStagingIsSerializedAcrossProcesses 验证多个进程同时在密钥环里 stage 新密钥时不会互相覆盖。
func TestStagingIsSerializedAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	ks1, err := keystore.NewFile(filepath.Join(dir, "keys.json"), "pw", 1)
	if err != nil {
		t.Fatal(err)
	}
	ks2, err := keystore.NewFile(filepath.Join(dir, "keys.json"), "pw", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Init(dir, ks1, "笔记本"); err != nil {
		t.Fatal(err)
	}
	n1, err := Open(Options{DataDir: dir, Keys: ks1, DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer n1.Close()
	n2, err := Open(Options{DataDir: dir, Keys: ks2, DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()

	if err := n1.EnsureCircle(); err != nil {
		t.Fatal(err)
	}

	const numKeys = 10
	var wg sync.WaitGroup
	for i := 0; i < numKeys; i++ {
		wg.Add(1)
		go func(i int, n *Node, ks keystore.Store) {
			defer wg.Done()
			key, _ := newDiscoveryKey()
			if err := stageDiscoveryKey(ks, n.Store().WithWriteLock, key); err != nil {
				t.Errorf("stageDiscoveryKey %d: %v", i, err)
			}
		}(i, []*Node{n1, n2}[i%2], []keystore.Store{ks1, ks2}[i%2])
	}
	wg.Wait()

	ring, err := loadRing(ks1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ring) != numKeys {
		t.Fatalf("staged %d keys but ring contains %d", numKeys, len(ring))
	}
}

// TestMergeReportsReloadedChange 验证 mergeChain 在重新加载的变化时返回 changed=true。
func TestMergeReportsReloadedChange(t *testing.T) {
	laptop := newDevice(t, "笔记本")
	phone := withExtraMember(t, laptop, "手机")
	c, _ := laptop.n.snapshot()

	cli, err := Open(Options{DataDir: laptop.dir, Keys: laptop.keys, DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if err := cli.RemoveDevice(phone.KeyID); err != nil {
		t.Fatal(err)
	}

	// 对方在旧链上加了一台设备：与移除记录分叉。
	// mergeChain 应该返回 changed=true（因为重新加载了数据库里新加的移除记录），
	// 同时返回 ErrFork。
	peer, _ := chain.Load(c.Records())
	tablet, _ := identity.Generate()
	_ = peer.AddDevice(laptop.n.id, tablet.Public, "平板")
	changed, err := laptop.n.mergeChain(peer.Records())
	if !errors.Is(err, chain.ErrFork) {
		t.Fatalf("merge should fail with ErrFork, got %v", err)
	}
	if !changed {
		t.Fatal("merge must return changed=true when it reloaded the removal")
	}
}

package node

import (
	"errors"
	"testing"

	"portide/core/chain"
	"portide/core/identity"
	"portide/core/keystore"
	"portide/core/store"
)

func TestInitIsIdempotentAndOpenRequiresInit(t *testing.T) {
	dir := t.TempDir()
	keys := keystore.Memory{}
	if _, err := Open(Options{DataDir: dir, Keys: keys}); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Open before Init: err = %v", err)
	}
	a, _ := Init(dir, keys, "笔记本")
	b, _ := Init(dir, keys, "改名后")
	if a != b {
		t.Fatal("re-running Init must keep the same identity")
	}
	n, err := Open(Options{DataDir: dir, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if n.Name() != "改名后" || n.InCircle() {
		t.Fatalf("name=%q inCircle=%v", n.Name(), n.InCircle())
	}
}

func TestEnsureCircleCreatesCircleAndDiscoveryKey(t *testing.T) {
	dir := t.TempDir()
	keys := keystore.Memory{}
	if _, err := Init(dir, keys, "笔记本"); err != nil {
		t.Fatal(err)
	}
	n, err := Open(Options{DataDir: dir, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if _, err := n.Devices(); !errors.Is(err, ErrNotInCircle) {
		t.Fatalf("Devices before a circle exists: err = %v", err)
	}
	if err := n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	id := n.CircleID()
	if err := n.EnsureCircle(); err != nil || n.CircleID() != id {
		t.Fatal("EnsureCircle must be idempotent")
	}
	if key, err := keys.Get(keystore.DiscoveryKey); err != nil || len(key) != 32 {
		t.Fatalf("discovery key = %x, %v", key, err)
	}
	devs, err := n.Devices()
	if err != nil || len(devs) != 1 || !devs[0].Self || devs[0].Name != "笔记本" {
		t.Fatalf("Devices = %+v, %v", devs, err)
	}
	if n.DaemonRunning() {
		t.Fatal("no daemon heartbeat has been written yet")
	}

	// 重新打开后从数据库恢复设备圈。
	n2, err := Open(Options{DataDir: dir, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if n2.CircleID() != id {
		t.Fatal("circle not restored from the store")
	}
}

// 数据库里的链比内存里的旧时（例如轮询读库与合并新链并发），重新加载不能让内存里的链倒退。
func TestReloadCircleNeverShrinks(t *testing.T) {
	dir := t.TempDir()
	keys := keystore.Memory{}
	if _, err := Init(dir, keys, "笔记本"); err != nil {
		t.Fatal(err)
	}
	n, err := Open(Options{DataDir: dir, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err := n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	other, _ := identity.Generate()
	longer, err := chain.Load(n.chain.Records())
	if err != nil {
		t.Fatal(err)
	}
	if err := longer.AddDevice(n.id, other.Public, "台式机"); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	n.chain = longer // 内存比数据库新一条
	n.mu.Unlock()

	changed, err := n.reloadCircle()
	if err != nil || changed {
		t.Fatalf("reloadCircle: changed=%v err=%v", changed, err)
	}
	if n.chain.Len() != 2 {
		t.Fatalf("in-memory chain shrank to %d records", n.chain.Len())
	}
}

// Init 和 Open 必须发现密钥库与数据目录不匹配（例如数据目录路径或 PORTIDE_KEYSTORE 变了）。
func TestInitRefusesForeignKeystore(t *testing.T) {
	dir := t.TempDir()
	keysA := keystore.Memory{}
	if _, err := Init(dir, keysA, "笔记本"); err != nil {
		t.Fatal(err)
	}

	keysB := keystore.Memory{} // 空的，没有 keysA 的种子
	if _, err := Init(dir, keysB, "笔记本"); err == nil {
		t.Fatal("Init with a keystore missing the recorded key must fail")
	}

	other, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	keysC := keystore.Memory{}
	if err := keysC.Set(keystore.DeviceSeed, other.Seed()); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{DataDir: dir, Keys: keysC}); err == nil {
		t.Fatal("Open with a keystore holding a different identity must fail")
	}

	n, err := Open(Options{DataDir: dir, Keys: keysA})
	if err != nil {
		t.Fatalf("Open with the original keystore must still work: %v", err)
	}
	n.Close()
}

// 两端对同一对重复连接必须得出相同的取舍：先看发起方 KeyID，同一方发起时看双方相同的 TLS 导出值。
func TestPreferredIsConsistent(t *testing.T) {
	a := &memberConn{dialedBy: "AAA", tieKey: []byte{2}}
	b := &memberConn{dialedBy: "BBB", tieKey: []byte{1}}
	if !preferred(a, b) || preferred(b, a) {
		t.Fatal("the connection dialed by the lower KeyID must win")
	}
	c := &memberConn{dialedBy: "AAA", tieKey: []byte{1}}
	if !preferred(c, a) || preferred(a, c) {
		t.Fatal("same dialer: the lower tie key must win")
	}
	// 直连优先于经中继的连接，不管发起方和导出值如何（M2b）。
	r := &memberConn{dialedBy: "AAA", tieKey: []byte{0}, relayed: true}
	if !preferred(b, r) || preferred(r, b) {
		t.Fatal("a direct connection must win over a relayed one")
	}
}

// 设备名只能在加入设备圈之前设置；没有名字时不能加入（邀请方会拒绝空名字）。
func TestSetNameBeforeJoining(t *testing.T) {
	dir := t.TempDir()
	keys := keystore.Memory{}
	if _, err := Init(dir, keys, "临时"); err != nil {
		t.Fatal(err)
	}
	n, err := Open(Options{DataDir: dir, Keys: keys, ListenAddr: "127.0.0.1:0", DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	if err := n.SetName(""); err == nil {
		t.Fatal("an empty name must be rejected")
	}
	if err := n.SetName("我的 iPhone"); err != nil || n.Name() != "我的 iPhone" {
		t.Fatalf("SetName: %v, Name = %q", err, n.Name())
	}
	if v, _, _ := n.store.GetMeta(store.MetaDeviceName); v != "我的 iPhone" {
		t.Fatalf("stored name = %q", v)
	}
	// 没有名字（例如重装后数据目录是新的）：加入前就报错，不把空名字发给邀请方。
	n.name = ""
	if _, err := n.Join(t.Context(), "12345678", []string{"127.0.0.1:1"}, false); !errors.Is(err, ErrNoName) {
		t.Fatalf("Join without a name: err = %v, want ErrNoName", err)
	}
	n.name = "我的 iPhone"
	if err := n.EnsureCircle(); err != nil {
		t.Fatal(err)
	}
	if err := n.SetName("圈里改名"); !errors.Is(err, ErrInCircle) {
		t.Fatalf("SetName in a circle: err = %v, want ErrInCircle", err)
	}
}

package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMeta(t *testing.T) {
	s := open(t)
	if _, ok, err := s.GetMeta(MetaDeviceName); err != nil || ok {
		t.Fatalf("empty meta: ok=%v err=%v", ok, err)
	}
	_ = s.SetMeta(MetaDeviceName, "笔记本")
	_ = s.SetMeta(MetaDeviceName, "新名字")
	v, ok, err := s.GetMeta(MetaDeviceName)
	if err != nil || !ok || v != "新名字" {
		t.Fatalf("GetMeta = %q %v %v", v, ok, err)
	}
}

func TestChainReplace(t *testing.T) {
	s := open(t)
	if recs, err := s.LoadChain(); err != nil || len(recs) != 0 {
		t.Fatalf("empty chain: %v %v", recs, err)
	}
	_ = s.SaveChain([][]byte{[]byte("a"), []byte("b"), []byte("c")})
	_ = s.SaveChain([][]byte{[]byte("a"), []byte("b")})
	recs, err := s.LoadChain()
	if err != nil || len(recs) != 2 || string(recs[1]) != "b" {
		t.Fatalf("LoadChain = %q %v", recs, err)
	}
}

func TestAppendChain(t *testing.T) {
	s := open(t)
	if err := s.AppendChain(0, [][]byte{[]byte("a"), []byte("b")}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChain(2, [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
		t.Fatal(err)
	}
	// 读到的是 2 条、数据库里已经是 3 条：拒绝写入，不覆盖别人刚追加的记录。
	if err := s.AppendChain(2, [][]byte{[]byte("a"), []byte("b"), []byte("x")}); !errors.Is(err, ErrChainChanged) {
		t.Fatalf("stale append: err = %v, want ErrChainChanged", err)
	}
	recs, err := s.LoadChain()
	if err != nil || len(recs) != 3 || string(recs[2]) != "c" {
		t.Fatalf("LoadChain = %q %v", recs, err)
	}
}

func TestAppendChainChecksTheLastRecord(t *testing.T) {
	s := open(t)
	if err := s.AppendChain(0, [][]byte{[]byte("a"), []byte("b")}); err != nil {
		t.Fatal(err)
	}
	// 条数对得上、内容对不上（别的进程刚把分叉的后缀换掉了）：同样拒绝，免得接在别人的记录后面写坏链。
	if err := s.AppendChain(2, [][]byte{[]byte("a"), []byte("x"), []byte("y")}); !errors.Is(err, ErrChainChanged) {
		t.Fatalf("append on a different tail: err = %v, want ErrChainChanged", err)
	}
}

func TestReplaceChain(t *testing.T) {
	s := open(t)
	if err := s.AppendChain(0, [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
		t.Fatal(err)
	}
	old := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	// 数据库已经不是 old：拒绝。
	if err := s.ReplaceChain([][]byte{[]byte("a"), []byte("x"), []byte("c")}, [][]byte{[]byte("a")}); !errors.Is(err, ErrChainChanged) {
		t.Fatalf("stale replace: err = %v, want ErrChainChanged", err)
	}
	if err := s.ReplaceChain(old[:2], [][]byte{[]byte("a")}); !errors.Is(err, ErrChainChanged) {
		t.Fatalf("stale length: err = %v, want ErrChainChanged", err)
	}
	// 从分歧处换掉后缀，新链可以更短、一样长或更长。
	for _, next := range [][][]byte{
		{[]byte("a"), []byte("y")},
		{[]byte("a"), []byte("z"), []byte("w")},
		{[]byte("a"), []byte("z"), []byte("w"), []byte("v")},
	} {
		if err := s.ReplaceChain(old, next); err != nil {
			t.Fatal(err)
		}
		recs, err := s.LoadChain()
		if err != nil || len(recs) != len(next) {
			t.Fatalf("LoadChain = %q %v, want %q", recs, err, next)
		}
		for i := range next {
			if string(recs[i]) != string(next[i]) {
				t.Fatalf("LoadChain = %q, want %q", recs, next)
			}
		}
		old = next
	}
}

func TestPeers(t *testing.T) {
	s := open(t)
	now := time.UnixMilli(time.Now().UnixMilli())
	_ = s.UpsertPeer(Peer{KeyID: "K1", Addr: "192.168.1.5:7443", LastSeen: now, Online: true})
	_ = s.UpsertPeer(Peer{KeyID: "K1", Addr: "192.168.1.6:7443", LastSeen: now, Online: true})
	_ = s.SetAllOffline()
	peers, err := s.Peers()
	if err != nil || len(peers) != 1 {
		t.Fatalf("Peers = %+v %v", peers, err)
	}
	p := peers[0]
	if p.Addr != "192.168.1.6:7443" || p.Online || !p.LastSeen.Equal(now) {
		t.Fatalf("peer = %+v", p)
	}
}

func TestPeerFeatures(t *testing.T) {
	s := open(t)
	now := time.UnixMilli(time.Now().UnixMilli())
	_ = s.UpsertPeer(Peer{KeyID: "K1", Addr: "192.168.1.5:7443", LastSeen: now, Online: true})
	if err := s.SetPeerFeatures("K1", []string{"rename", "x"}); err != nil {
		t.Fatal(err)
	}
	_ = s.SetPeerFeatures("K2", []string{"rename"})
	_ = s.UpsertPeer(Peer{KeyID: "K1", Addr: "192.168.1.6:7443", LastSeen: now, Online: false})
	_ = s.SetPeerFeatures("K3", nil)
	f, err := s.PeerFeatures()
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 2 || strings.Join(f["K1"], ",") != "rename,x" || strings.Join(f["K2"], ",") != "rename" {
		t.Fatalf("PeerFeatures = %v", f)
	}
	peers, _ := s.Peers()
	for _, p := range peers {
		if p.KeyID == "K1" && p.Addr != "192.168.1.6:7443" {
			t.Fatalf("upsert must keep features and update addr: %+v", p)
		}
	}
}

func TestPairSessionLifecycle(t *testing.T) {
	s := open(t)
	first, _ := s.CreatePairSession("11112222", time.Minute)
	second, _ := s.CreatePairSession("33334444", time.Minute)

	if old, _ := s.GetPairSession(first); old.Status != PairFailed || old.Error != "superseded" {
		t.Fatalf("first session = %+v, want superseded", old)
	}
	ps, err := s.PendingPairSession(time.Now())
	if err != nil || ps == nil || ps.ID != second || ps.Code != "33334444" {
		t.Fatalf("pending = %+v %v", ps, err)
	}
	if n, _ := s.IncPairAttempts(second); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
	_ = s.FinishPairSession(second, PairDone, "台式机", "")
	done, _ := s.GetPairSession(second)
	if done.Status != PairDone || done.JoinerName != "台式机" || done.Code != "" {
		t.Fatalf("finished session = %+v", done)
	}
	if ps, _ := s.PendingPairSession(time.Now()); ps != nil {
		t.Fatalf("no session should be pending, got %+v", ps)
	}
}

// 配对成功时记下结果模式（加入 / 反向加入），界面据此显示"对方加入了本机"还是"本机加入了对方"。
func TestCompletePairSessionRecordsMode(t *testing.T) {
	s := open(t)
	id, _ := s.CreatePairSession("77778888", time.Minute)
	if err := s.CompletePairSession(id, "手机", "adopt", 0); err != nil {
		t.Fatal(err)
	}
	ps, err := s.GetPairSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Status != PairDone || ps.Mode != "adopt" || ps.Code != "" || ps.JoinerName != "手机" {
		t.Fatalf("completed session = %+v", ps)
	}
}

func TestPairSessionExpires(t *testing.T) {
	s := open(t)
	id, _ := s.CreatePairSession("55556666", time.Minute)
	if ps, _ := s.PendingPairSession(time.Now().Add(2 * time.Minute)); ps != nil {
		t.Fatalf("expired session returned: %+v", ps)
	}
	if ps, _ := s.GetPairSession(id); ps.Status != PairFailed || ps.Error != "expired" {
		t.Fatalf("session = %+v, want expired", ps)
	}
}

func TestTwoHandlesShareData(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir)
	defer a.Close()
	b, _ := Open(dir)
	defer b.Close()
	_ = a.SetMeta(MetaDaemonHeartbeat, "123")
	if v, ok, _ := b.GetMeta(MetaDaemonHeartbeat); !ok || v != "123" {
		t.Fatalf("second handle sees %q %v", v, ok)
	}
}

func TestWithWriteLock(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	done := make(chan bool, 1)
	go func() {
		// 在 s2 上尝试 AppendChain，应该被锁阻挡
		time.Sleep(100 * time.Millisecond)
		if err := s2.AppendChain(0, [][]byte{[]byte("a"), []byte("b")}); err != nil {
			t.Errorf("AppendChain from s2: %v", err)
		}
		done <- true
	}()

	start := time.Now()
	s1.WithWriteLock(func() error {
		// 持有锁 200ms，期间 s2 的 AppendChain 应该被阻挡
		time.Sleep(200 * time.Millisecond)
		return nil
	})

	// 等 goroutine 完成
	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed < 150*time.Millisecond {
			t.Fatalf("AppendChain completed too quickly (%.0fms), should have waited for lock", elapsed.Seconds()*1000)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AppendChain never completed")
	}
}

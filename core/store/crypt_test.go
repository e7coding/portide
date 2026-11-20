package store

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, DBKeySize) }

func TestSealOpen(t *testing.T) {
	s := open(t)
	if err := s.UseKey(testKey(1)); err != nil {
		t.Fatal(err)
	}
	plain := []byte("照片/2026/猫.jpg")
	a, b := s.seal("incoming.path", plain), s.seal("incoming.path", plain)
	if !isSealed(a) || bytes.Contains(a, []byte("猫.jpg")) || bytes.Equal(a, b) {
		t.Fatalf("sealed value leaks plaintext or reuses a nonce: %x / %x", a, b)
	}
	if got, err := s.open("incoming.path", a); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := s.open("outgoing.offer", a); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("moved to another field: %v", err)
	}
	bad := append([]byte(nil), a...)
	bad[len(bad)-1] ^= 1
	if _, err := s.open("incoming.path", bad); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := s.open("incoming.path", sealMagic); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated: %v", err)
	}
	// 升级前写入的明文照常读出；空值保持为空。
	if got, err := s.open("incoming.path", []byte("a.txt")); err != nil || string(got) != "a.txt" {
		t.Fatalf("legacy plaintext: %q, %v", got, err)
	}
	if v := s.seal("incoming.path", nil); len(v) != 0 {
		t.Fatalf("empty value sealed: %x", v)
	}
}

func TestOpenWithoutKey(t *testing.T) {
	s := open(t)
	if v := s.seal("inbox.summary", []byte("x")); string(v) != "x" {
		t.Fatalf("without a key values stay plaintext: %q", v)
	}
	sealed := append(append([]byte(nil), sealMagic...), make([]byte, 64)...)
	if _, err := s.open("inbox.summary", sealed); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v", err)
	}
}

// 数据目录绑定第一次用过的数据库密钥：换一把密钥打开会被拒绝。
func TestUseKeyBindsDataDirectory(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if bound, _ := s.KeyBound(); bound {
		t.Fatal("fresh database already bound")
	}
	if err := s.UseKey(testKey(1)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if bound, _ := s.KeyBound(); !bound {
		t.Fatal("binding not recorded")
	}
	if err := s.UseKey(testKey(2)); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("other key: %v", err)
	}
	if err := s.UseKey(testKey(1)); err != nil {
		t.Fatalf("same key: %v", err)
	}
	if err := s.UseKey([]byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
}

// rawContains 报告数据库文件里任何表的任何值是否包含 needle（直接看磁盘上的原始内容）。
func rawContains(t *testing.T, s *Store, needle string) bool {
	t.Helper()
	tables, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		_ = tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	for _, name := range names {
		rows, err := s.db.Query(`SELECT * FROM "` + name + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = rows.Scan(ptrs...)
			for _, v := range vals {
				switch x := v.(type) {
				case []byte:
					if bytes.Contains(x, []byte(needle)) {
						rows.Close()
						return true
					}
				case string:
					if bytes.Contains([]byte(x), []byte(needle)) {
						rows.Close()
						return true
					}
				}
			}
		}
		rows.Close()
	}
	return false
}

// 设置密钥之后，文件名、路径、错误信息和报价摘要在数据库里都只以密文出现，读回来是原文。
func TestSensitiveFieldsAreEncrypted(t *testing.T) {
	s := open(t)
	if err := s.UseKey(testKey(1)); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateOutgoing("PEER", []byte{1}, []byte("offer:机密.pdf"), []byte(`[{"path":"/home/me/机密.pdf"}]`), 3)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.UpdateOutgoing(id, SendFailed, "文件在发送前被移动或删除：/home/me/机密.pdf")
	k := IncomingKey{PeerKey: "PEER", TransferID: "ab"}
	_ = s.AddIncoming(k, "d", map[int]string{0: "机密/报告.pdf", 1: ""})
	inboxID, _ := s.CreateInbox("手机", "机密/报告.pdf（1 个文件）", 3, time.Minute)

	if rawContains(t, s, "机密") {
		t.Fatal("plaintext file name found in the database file")
	}
	o, err := s.GetOutgoing(id)
	if err != nil || string(o.Offer) != "offer:机密.pdf" || string(o.Sources) != `[{"path":"/home/me/机密.pdf"}]` || o.Error != "文件在发送前被移动或删除：/home/me/机密.pdf" {
		t.Fatalf("outgoing = %+v, %v", o, err)
	}
	items, err := s.IncomingItems(k)
	if err != nil || items[0].Path != "机密/报告.pdf" || items[1].Path != "" {
		t.Fatalf("incoming = %+v, %v", items, err)
	}
	if tops, err := s.IncomingTops(); err != nil || len(tops) != 1 || tops[0] != "机密" {
		t.Fatalf("tops = %v, %v", tops, err)
	}
	if e, err := s.GetInbox(inboxID); err != nil || e.Summary != "机密/报告.pdf（1 个文件）" {
		t.Fatalf("inbox = %+v, %v", e, err)
	}
}

// 升级前写入的明文：照常能读，SealLegacy 之后磁盘上只剩密文。
func TestSealLegacy(t *testing.T) {
	s := open(t)
	id, _ := s.CreateOutgoing("PEER", []byte{1}, []byte("旧报价"), []byte("旧来源"), 1)
	_ = s.UpdateOutgoing(id, SendInterrupted, "旧错误")
	k := IncomingKey{PeerKey: "PEER", TransferID: "ab"}
	_ = s.AddIncoming(k, "d", map[int]string{0: "旧路径.txt"})
	_, _ = s.CreateInbox("手机", "旧摘要", 1, time.Minute)
	if err := s.SealLegacy(); !errors.Is(err, ErrLocked) {
		t.Fatalf("SealLegacy without a key: %v", err)
	}

	if err := s.UseKey(testKey(1)); err != nil {
		t.Fatal(err)
	}
	if o, err := s.GetOutgoing(id); err != nil || string(o.Offer) != "旧报价" {
		t.Fatalf("legacy read before sealing: %+v, %v", o, err)
	}
	if err := s.SealLegacy(); err != nil {
		t.Fatal(err)
	}
	if err := s.SealLegacy(); err != nil { // 再执行一次什么都不改
		t.Fatal(err)
	}
	for _, w := range []string{"旧报价", "旧来源", "旧错误", "旧路径", "旧摘要"} {
		if rawContains(t, s, w) {
			t.Fatalf("%q still in plaintext", w)
		}
	}
	o, err := s.GetOutgoing(id)
	if err != nil || string(o.Offer) != "旧报价" || string(o.Sources) != "旧来源" || o.Error != "旧错误" {
		t.Fatalf("after sealing: %+v, %v", o, err)
	}
	if items, _ := s.IncomingItems(k); items[0].Path != "旧路径.txt" {
		t.Fatalf("incoming after sealing: %+v", items)
	}
}

// 升级前（secure_delete 还没打开时）的删除留在数据库文件的空闲页里；SealLegacy 末尾的 VACUUM
// 要把它们连同空闲页一起清掉，光靠改写现存行是不够的。
func TestSealLegacyWipesDeletedPlaintext(t *testing.T) {
	const marker = "DELETED-SECRET-MARKER"
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "portide.db")

	// 先用 store.Open 建好 schema，再关掉，换一条不带 secure_delete 的原始连接写明文、删除它，
	// 模拟升级前 secure_delete 还是默认值（关）时的旧行为。
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA secure_delete=0`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := raw.Exec(`INSERT INTO inbox (peer_name, summary, total, status, expires_at) VALUES (?, ?, 1, 'ready', 0)`,
			"手机", marker+"-"+string(rune('A'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`DELETE FROM inbox`); err != nil {
		t.Fatal(err)
	}
	// 把刚才的改动从 WAL 写回主文件（secure_delete 关着，被删掉的行内容不会被清零），
	// 这样 needle 就留在 portide.db 本身里，不只是 WAL 里。
	if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	if !fileOrWALContains(t, dir, marker) {
		t.Fatal("setup failed: marker is not in the database file before sealing")
	}

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UseKey(testKey(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.SealLegacy(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if fileOrWALContains(t, dir, marker) {
		t.Fatal("deleted plaintext still readable on disk after SealLegacy")
	}
}

// fileOrWALContains 报告 dataDir 下的 portide.db 或 portide.db-wal 是否包含 needle。
func fileOrWALContains(t *testing.T, dataDir, needle string) bool {
	t.Helper()
	for _, name := range []string{"portide.db", "portide.db-wal"} {
		b, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(needle)) {
			return true
		}
	}
	return false
}

// lockedKeys 是并发安全的内存密钥库，记下生成过几次密钥。
type lockedKeys struct {
	mu      sync.Mutex
	key     []byte
	created int
}

func (k *lockedKeys) load() ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.key, k.key != nil, nil
}

func (k *lockedKeys) create() ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.created++
	k.key = testKey(byte(k.created))
	return k.key, nil
}

// 守护进程和命令行第一次同时打开数据目录：只生成一把密钥，大家都用它。
func TestInitKeyConcurrentFirstOpen(t *testing.T) {
	dir := t.TempDir()
	keys := &lockedKeys{}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(dir)
			if err != nil {
				errs <- err
				return
			}
			defer s.Close()
			errs <- s.InitKey(keys.load, keys.create)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if keys.created != 1 {
		t.Fatalf("created %d keys, want 1", keys.created)
	}
}

func TestInitKeyMissingOrWrong(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	keys := &lockedKeys{}
	if err := s.InitKey(keys.load, keys.create); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	empty := &lockedKeys{}
	if err := s.InitKey(empty.load, empty.create); !errors.Is(err, ErrKeyMissing) || empty.created != 0 {
		t.Fatalf("missing key: %v (created %d)", err, empty.created)
	}
	other := &lockedKeys{key: testKey(9)}
	if err := s.InitKey(other.load, other.create); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("other key: %v", err)
	}
	if err := s.InitKey(keys.load, keys.create); err != nil {
		t.Fatalf("right key: %v", err)
	}
}

// 已经绑定过密钥时，InitKey 不能占着写锁去问密钥库（钥匙串弹窗、PBKDF2）——否则守护进程的
// 写操作要等 busy_timeout 超时才报错。用一个在 load 执行期间抢写锁的 SetMeta 验证这一点：
// 走写锁路径的话这里会 BUSY；走快速路径就不会。
func TestInitKeyBoundDoesNotHoldWriteLock(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	keys := &lockedKeys{}
	if err := first.InitKey(keys.load, keys.create); err != nil {
		t.Fatal(err)
	}

	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	var probeErr error
	loadDuringWrite := func() ([]byte, bool, error) {
		probeErr = first.SetMeta("probe", "1")
		return keys.key, true, nil
	}
	if err := second.InitKey(loadDuringWrite, keys.create); err != nil {
		t.Fatalf("InitKey = %v", err)
	}
	if probeErr != nil {
		t.Fatalf("write on another handle blocked while InitKey ran: %v", probeErr)
	}
}

// 删除和改写的内容不能留在数据库文件里（secure_delete）。
func TestDeletedContentIsWiped(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.CreateInbox("手机", "要被删掉的摘要内容", 1, time.Minute)
	_ = s.DeleteInbox(id)
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	b, err := os.ReadFile(filepath.Join(dir, "portide.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("要被删掉的摘要内容")) {
		t.Fatal("deleted content still readable in the database file")
	}
}

func TestSecretMeta(t *testing.T) {
	s := open(t)
	if err := s.UseKey(testKey(3)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetSecretMeta(MetaRelayConfig); ok || err != nil {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
	if err := s.SetSecretMeta(MetaRelayConfig, "portide-relay://秘密令牌"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.GetSecretMeta(MetaRelayConfig)
	if err != nil || !ok || v != "portide-relay://秘密令牌" {
		t.Fatalf("GetSecretMeta = %q %v %v", v, ok, err)
	}
	if rawContains(t, s, "秘密令牌") {
		t.Fatal("secret meta must be stored encrypted")
	}
	if err := s.DeleteMeta(MetaRelayConfig); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetSecretMeta(MetaRelayConfig); ok {
		t.Fatal("deleted meta must be gone")
	}
}

package node

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"portide/core/keystore"
	"portide/core/store"
)

// dbFileContains 报告数据目录里的数据库文件（含 WAL）是否出现 needle。
func dbFileContains(t *testing.T, dir, needle string) bool {
	t.Helper()
	for _, name := range []string{"portide.db", "portide.db-wal"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(needle)) {
			return true
		}
	}
	return false
}

// 排队中的任务带着文件名和本机路径：数据库文件里只能看到密文（设计方案 6.2）。
func TestQueuedTaskIsEncryptedAtRest(t *testing.T) {
	laptop, desktop := pairOffline(t)
	src := filepath.Join(t.TempDir(), "机密报告.pdf")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := plan(t, "", nil)
	if err := p.AddPath(src); err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.n.QueueSend(desktop.n.Identity().KeyID, p); err != nil {
		t.Fatal(err)
	}
	if dbFileContains(t, laptop.dir, "机密报告") {
		t.Fatal("file name stored in plaintext")
	}
	if _, err := laptop.n.Store().ListOutgoing(); err != nil {
		t.Fatalf("queued task unreadable through the store: %v", err)
	}
}

// 升级前（没有数据库密钥）写入的明文：第一次用新版本打开时生成密钥并改写成密文。
func TestOpenSealsLegacyPlaintext(t *testing.T) {
	dir := t.TempDir()
	keys := keystore.Memory{}
	if _, err := Init(dir, keys, "笔记本"); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateInbox("手机", "旧版本留下的摘要", 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	st.Close()

	n, err := Open(Options{DataDir: dir, Keys: keys, DisableMDNS: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if _, err := keys.Get(keystore.DatabaseKey); err != nil {
		t.Fatalf("database key not created: %v", err)
	}
	if e, err := n.Store().PendingInbox(time.Now()); err != nil || len(e) != 1 || e[0].Summary != "旧版本留下的摘要" {
		t.Fatalf("inbox after sealing: %+v, %v", e, err)
	}
	// 数据目录还开着（守护进程在运行）时，磁盘上——主文件和 WAL——就已经找不到明文。
	if dbFileContains(t, dir, "旧版本留下的摘要") {
		t.Fatal("legacy plaintext still on disk")
	}
}

// 密钥库换了（数据库密钥丢失或不是同一把）：打开时报错，而不是悄悄换一把新密钥。
func TestDatabaseKeyMustMatch(t *testing.T) {
	d := newDevice(t, "笔记本")
	d.n.Close()
	seed, _ := d.keys.Get(keystore.DeviceSeed)
	lost := keystore.Memory{}
	_ = lost.Set(keystore.DeviceSeed, seed)
	if _, err := Open(Options{DataDir: d.dir, Keys: lost, DisableMDNS: true}); !errors.Is(err, store.ErrKeyMissing) {
		t.Fatalf("missing key: %v", err)
	}
	_ = lost.Set(keystore.DatabaseKey, bytes.Repeat([]byte{7}, store.DBKeySize))
	if _, err := Open(Options{DataDir: d.dir, Keys: lost, DisableMDNS: true}); !errors.Is(err, store.ErrWrongKey) {
		t.Fatalf("wrong key: %v", err)
	}
	n, err := Open(Options{DataDir: d.dir, Keys: d.keys, DisableMDNS: true})
	if err != nil {
		t.Fatalf("original keystore: %v", err)
	}
	n.Close()
}

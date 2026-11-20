package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// rawDB 直接打开数据目录里的数据库文件，用来构造旧版本的数据库。
func rawDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "portide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// M1b-2 的数据库没有版本号（user_version 为 0），发送任务表也没有队列的列：打开时应升级并保留已有数据。
func TestMigrateFromUnversionedDatabase(t *testing.T) {
	dir := t.TempDir()
	old := rawDB(t, dir)
	if _, err := old.Exec(schemaV1 + transferSchemaV1); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO outgoing (peer_key, transfer_id, offer, sources, status, updated_at) VALUES ('P', x'01', x'', '[]', 'pending', 1700000000000)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v := userVersion(t, s.db); v != len(migrations) || v != 8 {
		t.Fatalf("user_version = %d, want %d (8)", v, len(migrations))
	}
	tasks, err := s.ListOutgoing()
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %v, %v", tasks, err)
	}
	if o := tasks[0]; !o.CreatedAt.Equal(time.UnixMilli(1700000000000)) || o.Attempts != 0 || !o.NextAt.IsZero() {
		t.Fatalf("migrated task = %+v", o)
	}
	// 再打开一次不会重复执行迁移。
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestMigrateRefusesNewerDatabase(t *testing.T) {
	dir := t.TempDir()
	db := rawDB(t, dir)
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(dir)
	if err == nil {
		s.Close()
		t.Fatal("opened a database from a newer version")
	}
	if !errors.Is(err, ErrNewerDatabase) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNewerDatabase)", err)
	}
}

// 守护进程和命令行可能同时打开一个新数据目录：迁移只能执行一次，不能报"列已存在"。
func TestConcurrentOpenMigratesOnce(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(dir)
			if err == nil {
				s.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

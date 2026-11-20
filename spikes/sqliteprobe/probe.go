// Package sqliteprobe 验证纯 Go 的 modernc.org/sqlite 能否在目标平台（重点是 iOS）打开数据库、启用 WAL 并读写。
// M0 一次性验证代码。
package sqliteprobe

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Probe 在 dir 下打开 probe.db，插入 1000 行并返回 SQLite 版本、日志模式、总行数和插入耗时。
func Probe(dir string) (string, error) {
	dsn := "file:" + filepath.Join(dir, "probe.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", err
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY,
		body BLOB NOT NULL,
		created_at INTEGER NOT NULL)`); err != nil {
		return "", err
	}

	start := time.Now()
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	stmt, err := tx.Prepare(`INSERT INTO messages (body, created_at) VALUES (?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return "", err
	}
	for i := 0; i < 1000; i++ {
		if _, err := stmt.Exec([]byte(fmt.Sprintf("message %d", i)), time.Now().UnixNano()); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return "", err
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		return "", err
	}
	elapsed := time.Since(start)

	var version, mode string
	var rows int
	if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&version); err != nil {
		return "", err
	}
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		return "", err
	}
	if err := db.QueryRow(`SELECT count(*) FROM messages`).Scan(&rows); err != nil {
		return "", err
	}
	return fmt.Sprintf("sqlite=%s journal=%s rows=%d insert1000=%s", version, mode, rows, elapsed.Round(time.Microsecond)), nil
}

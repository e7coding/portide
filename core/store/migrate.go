package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migrations 依次把数据库升级到最新版本，当前版本号记在 PRAGMA user_version。
// 只能在末尾追加新的迁移，已发布的迁移不能修改。
//
// 版本 1 是 M1b-2 及之前的全部表：都用 CREATE TABLE IF NOT EXISTS，
// 所以对 user_version 为 0 的旧数据库（那时还没有版本号）也可以安全执行。
//
// 已发布（迁移 1）：不要修改，新的表或列请在 migrations 末尾追加迁移。
var migrations = []string{
	// 1：M1a–M1b-2
	schemaV1 + transferSchemaV1,
	// 2：M1b-3a 离线队列——任务的创建时间、失败次数和下次重试时间
	`ALTER TABLE outgoing ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE outgoing ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE outgoing ADD COLUMN next_at INTEGER NOT NULL DEFAULT 0;
UPDATE outgoing SET created_at = updated_at;`,
	// 3：M1b-3b 本地历史——正文（文本、文件清单、保存位置、错误）加密保存，其余字段用于排序和筛选
	`CREATE TABLE messages (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	direction  TEXT NOT NULL,
	peer_key   TEXT NOT NULL,
	status     TEXT NOT NULL,
	files      INTEGER NOT NULL DEFAULT 0,
	texts      INTEGER NOT NULL DEFAULT 0,
	total      INTEGER NOT NULL DEFAULT 0,
	body       BLOB NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX messages_created ON messages (created_at);`,
	// 4：M3b 暂停发送——暂停标记（和 cancel 一样由界面或命令行写、守护进程读）和最近一次继续的时间（排队期限从这里重新计算）
	`ALTER TABLE outgoing ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE outgoing ADD COLUMN resumed_at INTEGER NOT NULL DEFAULT 0;`,
	// 5：M3b 停止接收——记住被本机停止的接收（发送方 KeyID + 传输 ID），对方再报价时直接拒绝
	`CREATE TABLE stopped_incoming (
	peer_key    TEXT NOT NULL,
	transfer_id TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	PRIMARY KEY (peer_key, transfer_id)
);`,
	// 6：M3c 改名——每台设备在握手里声明支持的功能（逗号分隔），全员都支持 "rename" 才允许改名
	`ALTER TABLE peers ADD COLUMN features TEXT NOT NULL DEFAULT '';`,
	// 7：M5a 单机即加入——配对成功时记下结果模式（join / adopt），界面据此说明是谁加入了谁
	`ALTER TABLE pair_sessions ADD COLUMN mode TEXT NOT NULL DEFAULT '';`,
	// 8：合并设备圈的迁移记录（单行表），以及配对会话记录随加入方带进来的设备数。
	`CREATE TABLE circle_moves (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	old_circle TEXT NOT NULL,
	old_chain  BLOB,
	brought    TEXT NOT NULL DEFAULT '',
	pending    TEXT NOT NULL DEFAULT '',
	elsewhere  TEXT NOT NULL DEFAULT '',
	expires_at INTEGER NOT NULL
);
ALTER TABLE pair_sessions ADD COLUMN brought INTEGER NOT NULL DEFAULT 0;`,
}

// migrate 把数据库升级到最新版本。守护进程和命令行可能同时打开数据库，
// 所以在 BEGIN IMMEDIATE 事务里重新读取版本号，保证每个迁移只执行一次。
func migrate(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()
	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("store: database version %d is newer than this program (%d): %w", version, len(migrations), ErrNewerDatabase)
	}
	for v := version; v < len(migrations); v++ {
		if _, err := conn.ExecContext(ctx, migrations[v]); err != nil {
			return fmt.Errorf("store: migrate to version %d: %w", v+1, err)
		}
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations))); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

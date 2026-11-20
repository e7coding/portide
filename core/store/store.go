// Package store 是设备的本地数据库（SQLite，WAL 模式）。
// 守护进程和命令行进程通过同一个数据库协作，不需要额外的进程间通信。
package store

import (
	"bytes"
	"context"
	"crypto/cipher"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// 元数据键。
const (
	MetaDeviceName      = "device_name"
	MetaDaemonHeartbeat = "daemon_heartbeat" // 守护进程最近一次心跳（Unix 毫秒）
	MetaDaemonAddr      = "daemon_addr"      // 守护进程监听地址
	MetaKeyID           = "key_id"           // init 时记录的设备 KeyID，用来发现密钥库与数据目录不匹配
	MetaRelaySettings   = "relay_settings"   // 签名的中继列表（pb.RelaySettings 的 base64，加密保存）
	MetaRelayConfig     = "relay_config"     // 旧版的单个中继配置串，只在迁移时读一次（含访问令牌，加密保存，见 SetSecretMeta）
	MetaRelayStatus     = "relay_status"     // 守护进程与中继的连接状态（给命令行显示）
	MetaListenPort      = "listen_port"      // 守护进程监听的 UDP 端口；0 表示自动选择（M3c）
	MetaDeviceIcons     = "device_icons"     // 界面：本机给各设备选的图标（JSON，KeyID → 图标），不同步（M3c）
)

// 配对会话状态。
const (
	PairPending = "pending"
	PairDone    = "done"
	PairFailed  = "failed"
)

// 配对失败的原因（pair_sessions.error），界面据此显示中文说明。
const (
	PairErrTooManyAttempts = "too many attempts"
	PairErrCancelled       = "cancelled"
	PairErrOutdated        = "joiner outdated" // 加入方版本太旧，读不了含改名记录的链
)

// schemaV1 已发布（迁移 1）：不要修改，新的表或列请在 migrations 末尾追加迁移。
const schemaV1 = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS chain (
	seq    INTEGER PRIMARY KEY,
	record BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS peers (
	key_id    TEXT PRIMARY KEY,
	addr      TEXT NOT NULL DEFAULT '',
	last_seen INTEGER NOT NULL DEFAULT 0,
	online    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS pair_sessions (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	code        TEXT NOT NULL,
	expires_at  INTEGER NOT NULL,
	status      TEXT NOT NULL,
	attempts    INTEGER NOT NULL DEFAULT 0,
	joiner_name TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT ''
);
`

// Store 封装数据库连接。
type Store struct {
	db   *sql.DB
	aead cipher.AEAD // 数据库密钥；为 nil 时敏感字段按明文读写（见 crypt.go）

	onChange func(topic string) // 变化回调（见 change.go）
}

// Peer 是某个成员设备的连接状态。
type Peer struct {
	KeyID    string
	Addr     string
	LastSeen time.Time
	Online   bool
}

// PairSession 是一次"邀请新设备"的会话：命令行创建，守护进程执行。
type PairSession struct {
	ID         int64
	Code       string
	ExpiresAt  time.Time
	Status     string
	Attempts   int
	JoinerName string
	Error      string
	Mode       string // 成功时的结果模式（pairing.Mode：join / adopt / merge）；旧版本完成的会话为空
	Brought    int    // MERGE 时随加入方一起加进来的其他设备数
}

// ErrChainChanged 表示数据库里的签名链在读取之后被另一个进程改过（AppendChain 拒绝写入）。
var ErrChainChanged = errors.New("store: chain changed since it was read")

// ErrNewerDatabase 表示数据目录是用更新版本的程序创建的，本程序不认识它的迁移版本。
var ErrNewerDatabase = errors.New("store: database is from a newer version of portide")

// Open 打开（必要时创建）dataDir 下的 portide.db。
func Open(dataDir string) (*Store, error) {
	// modernc.org/sqlite 按固定顺序应用这些 pragma，与它们在 DSN 里的先后无关，所以这里的书写顺序
	// 不影响行为；真正解决"新数据库第一次切换 WAL 时 BUSY"的是下面的 pingRetry。
	// secure_delete：删除或改写的内容在文件里清零，而不是只把页标成空闲，用户删掉的历史就不会
	// 仍然能从数据库文件里读出来（设计方案 6.2）；它只管“打开之后”的删除和改写，升级前已经
	// 留在空闲页里的明文要靠 SealLegacy 末尾的 VACUUM 清掉（见 crypt.go）。
	dsn := "file:" + filepath.Join(dataDir, "portide.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=secure_delete(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := pingRetry(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// pingRetry 建立第一条连接。新数据库第一次切换到 WAL 时，如果另一个进程也正在打开它，
// SQLite 会直接返回 SQLITE_BUSY 而不等 busy_timeout；切换完成后 WAL 记在文件里，以后的连接不会再遇到。
func pingRetry(db *sql.DB) error {
	var err error
	for i := 0; i < 50; i++ {
		var se *sqlite.Error
		if err = db.Ping(); err == nil || !errors.As(err, &se) || se.Code()&0xff != sqlite3.SQLITE_BUSY {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// GetMeta 读取元数据；不存在时 ok 为 false。
func (s *Store) GetMeta(key string) (value string, ok bool, err error) {
	err = s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetMeta 写入元数据。
func (s *Store) SetMeta(key, value string) error {
	defer s.metaChanged(key)
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteMeta 删除一项元数据（不存在时什么都不做）。
func (s *Store) DeleteMeta(key string) error {
	defer s.metaChanged(key)
	_, err := s.db.Exec(`DELETE FROM meta WHERE key = ?`, key)
	return err
}

// SetSecretMeta 写入一项需要保密的元数据（例如中继令牌）：按字段加密保存，附加数据是 "meta." 加键名。
func (s *Store) SetSecretMeta(key, value string) error {
	defer s.metaChanged(key)
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, s.sealString("meta."+key, value))
	return err
}

// GetSecretMeta 读取 SetSecretMeta 写入的元数据；不存在时 ok 为 false。
func (s *Store) GetSecretMeta(key string) (value string, ok bool, err error) {
	var raw []byte
	err = s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	value, err = s.openString("meta."+key, raw)
	return value, err == nil, err
}

// LoadChain 按顺序返回签名链的全部记录；尚未加入设备圈时返回空切片。
func (s *Store) LoadChain() ([][]byte, error) {
	return loadChain(context.Background(), s.db)
}

// loadChain 按序读出签名链；q 可以是连接（事务内）或整个数据库。
func loadChain(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([][]byte, error) {
	rows, err := q.QueryContext(ctx, `SELECT record FROM chain ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var rec []byte
		if err := rows.Scan(&rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// SaveChain 用 records 整体替换签名链。
func (s *Store) SaveChain(records [][]byte) error {
	defer s.changed(ChangeDevices)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM chain`); err != nil {
		return err
	}
	for i, rec := range records {
		if _, err := tx.Exec(`INSERT INTO chain (seq, record) VALUES (?, ?)`, i, rec); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AppendChain 把 records 中第 prevLen 条之后的记录追加到签名链：只有数据库里的链仍是 prevLen 条时才写入，
// 否则返回 ErrChainChanged。守护进程和命令行都会改签名链（合并对方的更新、移除设备），
// 整体替换会悄悄覆盖另一方刚写入的记录；records 必须是读到的那 prevLen 条的延伸。
func (s *Store) AppendChain(prevLen int, records [][]byte) error {
	defer s.changed(ChangeDevices)
	if prevLen > len(records) {
		return errors.New("store: appended chain is shorter than the stored one")
	}
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// 写锁内先核对条数再追加：两个进程同时追加时只有一方成功。
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM chain`).Scan(&n); err != nil {
		return err
	}
	if n != prevLen {
		return ErrChainChanged
	}
	// 条数相同还要核对最后一条：别的进程可能刚把分叉的后缀换掉（ReplaceChain），不能接在别人的记录后面。
	if prevLen > 0 {
		var last []byte
		if err := conn.QueryRowContext(ctx, `SELECT record FROM chain WHERE seq = ?`, prevLen-1).Scan(&last); err != nil {
			return err
		}
		if !bytes.Equal(last, records[prevLen-1]) {
			return ErrChainChanged
		}
	}
	for i := prevLen; i < len(records); i++ {
		if _, err := conn.ExecContext(ctx, `INSERT INTO chain (seq, record) VALUES (?, ?)`, i, records[i]); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

// ReplaceChain 把数据库里的链从 old 换成 records：两者从第一条不同的记录起替换（自动收敛改名分叉时，本机分歧的后缀被胜出的链取代）。
// 数据库里的链已经不是 old 时返回 ErrChainChanged。
func (s *Store) ReplaceChain(old, records [][]byte) error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
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
	stored, err := loadChain(ctx, conn)
	if err != nil {
		return err
	}
	if !slices.EqualFunc(stored, old, bytes.Equal) {
		return ErrChainChanged
	}
	k := 0
	for k < len(old) && k < len(records) && bytes.Equal(old[k], records[k]) {
		k++
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM chain WHERE seq >= ?`, k); err != nil {
		return err
	}
	for i := k; i < len(records); i++ {
		if _, err := conn.ExecContext(ctx, `INSERT INTO chain (seq, record) VALUES (?, ?)`, i, records[i]); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	s.changed(ChangeDevices)
	return nil
}

// WithWriteLock 在数据库写锁（BEGIN IMMEDIATE）内执行 fn，用作守护进程和命令行之间的互斥锁：
// 密钥库没有跨进程的锁，读改写密钥环必须在这里面做。fn 里不能再写数据库（同一进程的另一条连接会等锁直到超时）。
func (s *Store) WithWriteLock(fn func() error) error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(ctx, `ROLLBACK`) }()
	return fn()
}

// UpsertPeer 写入成员设备的连接状态。
func (s *Store) UpsertPeer(p Peer) error {
	defer s.changed(ChangeDevices)
	_, err := s.db.Exec(`INSERT INTO peers (key_id, addr, last_seen, online) VALUES (?, ?, ?, ?)
		ON CONFLICT(key_id) DO UPDATE SET addr = excluded.addr, last_seen = excluded.last_seen, online = excluded.online`,
		p.KeyID, p.Addr, p.LastSeen.UnixMilli(), boolInt(p.Online))
	return err
}

// SetPeerFeatures 记下设备 keyID 在握手里声明的功能；没有这台设备的记录时先建一条离线的。
func (s *Store) SetPeerFeatures(keyID string, features []string) error {
	res, err := s.db.Exec(`INSERT INTO peers (key_id, addr, last_seen, online, features) VALUES (?, '', 0, 0, ?)
		ON CONFLICT(key_id) DO UPDATE SET features = excluded.features WHERE features IS NOT excluded.features`,
		keyID, strings.Join(features, ","))
	if err != nil {
		return err
	}
	// 能不能改名取决于各设备声明的功能：变了才通知界面重新判断。
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		s.changed(ChangeDevices)
	}
	return nil
}

// PeerFeatures 返回每台设备最近一次声明的功能（从没握过手的设备不在其中）。
func (s *Store) PeerFeatures() (map[string][]string, error) {
	rows, err := s.db.Query(`SELECT key_id, features FROM peers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var key, f string
		if err := rows.Scan(&key, &f); err != nil {
			return nil, err
		}
		if f != "" {
			out[key] = strings.Split(f, ",")
		}
	}
	return out, rows.Err()
}

// SetAllOffline 把所有设备标记为离线（守护进程启动和退出时调用）。
func (s *Store) SetAllOffline() error {
	defer s.changed(ChangeDevices)
	_, err := s.db.Exec(`UPDATE peers SET online = 0`)
	return err
}

// Peers 返回所有记录过的设备状态。
func (s *Store) Peers() ([]Peer, error) {
	rows, err := s.db.Query(`SELECT key_id, addr, last_seen, online FROM peers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		var lastSeen int64
		var online int
		if err := rows.Scan(&p.KeyID, &p.Addr, &lastSeen, &online); err != nil {
			return nil, err
		}
		p.LastSeen = time.UnixMilli(lastSeen)
		p.Online = online == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreatePairSession 创建新的配对会话，并作废之前所有未完成的会话。
func (s *Store) CreatePairSession(code string, ttl time.Duration) (int64, error) {
	defer s.changed(ChangePairing)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE pair_sessions SET status = ?, error = 'superseded' WHERE status = ?`, PairFailed, PairPending); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO pair_sessions (code, expires_at, status) VALUES (?, ?, ?)`,
		code, time.Now().Add(ttl).UnixMilli(), PairPending)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// PendingPairSession 返回当前有效的配对会话；没有时返回 nil。过期的会话会被标记为失败。
func (s *Store) PendingPairSession(now time.Time) (*PairSession, error) {
	if _, err := s.db.Exec(`UPDATE pair_sessions SET status = ?, error = 'expired' WHERE status = ? AND expires_at <= ?`,
		PairFailed, PairPending, now.UnixMilli()); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(`SELECT id FROM pair_sessions WHERE status = ? ORDER BY id DESC LIMIT 1`, PairPending)
	var id int64
	if err := row.Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return s.GetPairSession(id)
}

// GetPairSession 按 ID 读取配对会话。
func (s *Store) GetPairSession(id int64) (*PairSession, error) {
	var ps PairSession
	var expires int64
	err := s.db.QueryRow(`SELECT id, code, expires_at, status, attempts, joiner_name, error, mode, brought FROM pair_sessions WHERE id = ?`, id).
		Scan(&ps.ID, &ps.Code, &expires, &ps.Status, &ps.Attempts, &ps.JoinerName, &ps.Error, &ps.Mode, &ps.Brought)
	if err != nil {
		return nil, err
	}
	ps.ExpiresAt = time.UnixMilli(expires)
	return &ps, nil
}

// IncPairAttempts 记录一次失败的配对尝试，返回累计次数。
func (s *Store) IncPairAttempts(id int64) (int, error) {
	if _, err := s.db.Exec(`UPDATE pair_sessions SET attempts = attempts + 1 WHERE id = ?`, id); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRow(`SELECT attempts FROM pair_sessions WHERE id = ?`, id).Scan(&n)
	return n, err
}

// FinishPairSession 结束配对会话。完成后清空配对码，避免它留在磁盘上。
func (s *Store) FinishPairSession(id int64, status, joinerName, errMsg string) error {
	defer s.changed(ChangePairing)
	_, err := s.db.Exec(`UPDATE pair_sessions SET status = ?, joiner_name = ?, error = ?, code = '' WHERE id = ?`,
		status, joinerName, errMsg, id)
	return err
}

// CompletePairSession 把配对会话记为成功，记下对方设备名和结果模式（join / adopt / merge）和随之带进来的设备数，并清空配对码。
func (s *Store) CompletePairSession(id int64, joinerName, mode string, brought int) error {
	defer s.changed(ChangePairing)
	_, err := s.db.Exec(`UPDATE pair_sessions SET status = ?, joiner_name = ?, error = '', mode = ?, brought = ?, code = '' WHERE id = ?`,
		PairDone, joinerName, mode, brought, id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

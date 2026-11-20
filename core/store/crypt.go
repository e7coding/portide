package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// 字段加密（设计方案 6.2）：文本内容、文件名和路径用 XChaCha20-Poly1305 加密后再写进数据库；
// 时间、大小、状态这些用来排序和查询的字段保留明文。数据库密钥随机生成，保存在密钥库里（由 node 管理）。
//
// 密文格式：sealMagic（4 字节）‖ 24 字节随机 nonce ‖ 密文与认证标签；附加数据是字段名，
// 所以密文不能被挪到别的字段里冒充。没有 sealMagic 前缀的值是升级前写入的明文：照常读出，
// 并由 SealLegacy 统一改写成密文。空值保持为空，不加密。

// DBKeySize 是数据库密钥的长度。
const DBKeySize = chacha20poly1305.KeySize

// MetaDBKeyCheck 记录数据库密钥的校验值，用来发现密钥库与数据目录不匹配。
const MetaDBKeyCheck = "db_key_check"

// metaLegacyVacuumed 记录升级前明文是否已经被 VACUUM 清出数据库文件（每个数据目录只做一次）。
const metaLegacyVacuumed = "legacy_vacuumed"

var sealMagic = []byte("\x00fc1")

var (
	// ErrLocked 表示读到了加密字段，但没有设置数据库密钥。
	ErrLocked = errors.New("store: encrypted field but no database key")
	// ErrWrongKey 表示给出的数据库密钥不是这个数据目录用过的那一个。
	ErrWrongKey = errors.New("store: database key does not match this data directory")
	// ErrCorrupt 表示加密字段被篡改或损坏，无法解密。
	ErrCorrupt = errors.New("store: encrypted field cannot be decrypted")
	// ErrKeyMissing 表示数据目录已经绑定过数据库密钥，但密钥库里找不到它。
	ErrKeyMissing = errors.New("store: database key is missing from the keystore")
)

// UseKey 设置数据库密钥。必须在 Open 之后、开始并发使用之前调用。
// 第一次调用时在数据库里记下密钥的校验值；以后用别的密钥打开会返回 ErrWrongKey，
// 避免密钥库换了之后，新旧两把密钥写出的密文混在一个数据库里。
func (s *Store) UseKey(key []byte) error {
	if len(key) != DBKeySize {
		return fmt.Errorf("store: database key must be %d bytes, got %d", DBKeySize, len(key))
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	check := keyCheck(key)
	recorded, ok, err := s.GetMeta(MetaDBKeyCheck)
	if err != nil {
		return err
	}
	switch {
	case ok && recorded != check:
		return ErrWrongKey
	case !ok:
		if err := s.SetMeta(MetaDBKeyCheck, check); err != nil {
			return err
		}
	}
	s.aead = aead
	return nil
}

// InitKey 取出或生成数据库密钥，并把它绑定到这个数据目录（设计方案 6.2）。
//
// 数据目录已经绑定过密钥时走快速路径：只读一次 meta，不碰写锁——load 可能要敲 macOS 钥匙串或
// 算文件密钥库的 PBKDF2，命令行每次调用都带着写锁做这些事，会让守护进程的写操作在 busy_timeout
// 之后变成 SQLITE_BUSY。只有还没绑定过（第一次 Open）才需要写锁：整个过程在数据库写锁
// （BEGIN IMMEDIATE）里完成，守护进程和命令行第一次同时打开数据目录时，只有一方会生成密钥，
// 另一方等锁释放后读到的就是这一把——否则两边各生成一把，密钥库和数据库会记着不同的密钥。
//
// load 读取密钥库里的密钥，不存在时返回 found=false；create 生成并保存一把新密钥。
// 数据目录已经绑定过密钥、但密钥库里没有时返回 ErrKeyMissing；密钥不是绑定的那一把
// （包括长度不对的）时返回 ErrWrongKey。
func (s *Store) InitKey(load func() (key []byte, found bool, err error), create func() ([]byte, error)) error {
	if recorded, bound, err := s.GetMeta(MetaDBKeyCheck); err != nil {
		return err
	} else if bound {
		key, found, err := load()
		if err != nil {
			return err
		}
		if !found {
			return ErrKeyMissing
		}
		if len(key) != DBKeySize || keyCheck(key) != recorded {
			return ErrWrongKey
		}
		aead, err := chacha20poly1305.NewX(key)
		if err != nil {
			return err
		}
		s.aead = aead
		return nil
	}
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
	var recorded string
	err = conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, MetaDBKeyCheck).Scan(&recorded)
	bound := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	key, found, err := load()
	if err != nil {
		return err
	}
	switch {
	case !found && bound:
		return ErrKeyMissing
	case !found:
		if key, err = create(); err != nil {
			return err
		}
	}
	if bound && len(key) != DBKeySize {
		// 绑定过的数据目录收到长度不对的密钥，也是密钥不对，不是别的错误。
		return ErrWrongKey
	}
	if len(key) != DBKeySize {
		return fmt.Errorf("store: database key must be %d bytes, got %d", DBKeySize, len(key))
	}
	check := keyCheck(key)
	if bound && recorded != check {
		return ErrWrongKey
	}
	if !bound {
		if _, err := conn.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)`, MetaDBKeyCheck, check); err != nil {
			return err
		}
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	s.aead = aead
	return nil
}

// KeyBound 报告这个数据库是否已经绑定过数据库密钥（密钥库里的密钥丢了时，不能悄悄换一把新的）。
func (s *Store) KeyBound() (bool, error) {
	_, ok, err := s.GetMeta(MetaDBKeyCheck)
	return ok, err
}

// keyCheck 是密钥的校验值：不暴露密钥本身，只用来比较是不是同一把。
func keyCheck(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("portide-db-key-check-v1"))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// seal 加密一个字段；没有设置密钥时原样返回（测试和未加密的旧用法）。
func (s *Store) seal(field string, plain []byte) []byte {
	if len(plain) == 0 {
		return []byte{}
	}
	if s.aead == nil {
		return plain
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	_, _ = rand.Read(nonce)
	out := make([]byte, 0, len(sealMagic)+len(nonce)+len(plain)+s.aead.Overhead())
	out = append(append(out, sealMagic...), nonce...)
	return s.aead.Seal(out, nonce, plain, []byte(field))
}

// open 解密一个字段；不是密文的值（升级前写入的明文）原样返回。
func (s *Store) open(field string, v []byte) ([]byte, error) {
	if !isSealed(v) {
		return v, nil
	}
	if s.aead == nil {
		return nil, ErrLocked
	}
	rest := v[len(sealMagic):]
	if len(rest) < chacha20poly1305.NonceSizeX+s.aead.Overhead() {
		return nil, ErrCorrupt
	}
	plain, err := s.aead.Open(nil, rest[:chacha20poly1305.NonceSizeX], rest[chacha20poly1305.NonceSizeX:], []byte(field))
	if err != nil {
		return nil, ErrCorrupt
	}
	return plain, nil
}

func (s *Store) sealString(field, plain string) []byte { return s.seal(field, []byte(plain)) }

func (s *Store) openString(field string, v []byte) (string, error) {
	b, err := s.open(field, v)
	return string(b), err
}

func isSealed(v []byte) bool { return bytes.HasPrefix(v, sealMagic) }

// 加密字段的名字（同时用作附加数据）。
const (
	fieldOffer   = "outgoing.offer"
	fieldSources = "outgoing.sources"
	fieldError   = "outgoing.error"
	fieldPath    = "incoming.path"
	fieldSummary = "inbox.summary"
)

// SealLegacy 把升级前以明文写入的敏感字段改写成密文。设置密钥之后调用；没有明文时什么都不做。
// 守护进程和命令行可能同时调用：两边都只改写还没加密的值，结果都是合法的密文。
func (s *Store) SealLegacy() error {
	if s.aead == nil {
		return ErrLocked
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type change struct {
		query string
		args  []any
	}
	var changes []change
	collect := func(query string, scan func(*sql.Rows) error) error {
		rows, err := tx.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	// 只改写非空、还没加密的值。
	reseal := func(field string, v []byte) ([]byte, bool) {
		if len(v) == 0 || isSealed(v) {
			return v, false
		}
		return s.seal(field, v), true
	}
	err = collect(`SELECT id, offer, sources, error FROM outgoing`, func(r *sql.Rows) error {
		var id int64
		var offer, sources, errMsg []byte
		if err := r.Scan(&id, &offer, &sources, &errMsg); err != nil {
			return err
		}
		o, c1 := reseal(fieldOffer, offer)
		src, c2 := reseal(fieldSources, sources)
		e, c3 := reseal(fieldError, errMsg)
		if c1 || c2 || c3 {
			changes = append(changes, change{`UPDATE outgoing SET offer = ?, sources = ?, error = ? WHERE id = ?`, []any{o, src, e, id}})
		}
		return nil
	})
	if err != nil {
		return err
	}
	err = collect(`SELECT rowid, path FROM incoming_items`, func(r *sql.Rows) error {
		var id int64
		var p []byte
		if err := r.Scan(&id, &p); err != nil {
			return err
		}
		if v, changed := reseal(fieldPath, p); changed {
			changes = append(changes, change{`UPDATE incoming_items SET path = ? WHERE rowid = ?`, []any{v, id}})
		}
		return nil
	})
	if err != nil {
		return err
	}
	err = collect(`SELECT id, summary FROM inbox`, func(r *sql.Rows) error {
		var id int64
		var v []byte
		if err := r.Scan(&id, &v); err != nil {
			return err
		}
		if sealed, changed := reseal(fieldSummary, v); changed {
			changes = append(changes, change{`UPDATE inbox SET summary = ? WHERE id = ?`, []any{sealed, id}})
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := tx.Exec(c.query, c.args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(changes) > 0 {
		// 改写前的明文页还留在 WAL 文件里：马上写回主文件并截断 WAL（secure_delete 负责清零主文件里
		// 被替换掉的内容）。有别的进程正在读时截断会失败，那就等 SQLite 下一次自动检查点。
		_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	// secure_delete 只清零“以后”的删除和改写；升级前（secure_delete 还没打开时）删掉的文件名、
	// 文本摘要、报价，字节仍然留在数据库文件的空闲页里，grep 一下就能看到。VACUUM 把整个文件
	// 按现有内容重写一遍，不会带上那些空闲页，是清掉它们的唯一办法；每个数据目录只需要做一次，
	// 用 meta 记一个标记。失败就不设标记，下次打开再试——不能因为这个让 node.Open 失败。
	if v, ok, err := s.GetMeta(metaLegacyVacuumed); err == nil && (!ok || v != "1") {
		if _, err := s.db.Exec(`VACUUM`); err == nil {
			// 有别的连接正在读时检查点做不完（busy=1），旧的空闲页还留在主文件里：不设标记，下次打开再来。
			var busy, logFrames, done int
			if err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &done); err == nil && busy == 0 {
				_ = s.SetMeta(metaLegacyVacuumed, "1")
			}
		}
	}
	return nil
}

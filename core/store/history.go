package store

import (
	"database/sql"
	"errors"
	"time"
)

// MetaHistoryDays 是历史记录的自动清理天数；为空或 0 表示永久保留（设计方案 6.3）。
const MetaHistoryDays = "history_days"

// 历史记录的方向。
const (
	MessageIn  = "in"  // 收到
	MessageOut = "out" // 发出
)

// 历史记录的结果。
const (
	MessageDone      = "done"      // 全部送达 / 收齐
	MessagePartial   = "partial"   // 部分文件没有送达
	MessageFailed    = "failed"    // 没有送出（被拒绝、过期、源文件丢失……）
	MessageCancelled = "cancelled" // 发送方取消
)

// ErrNoMessage 表示没有这条历史记录。
var ErrNoMessage = errors.New("store: no such history entry")

const fieldBody = "messages.body"

// Message 是时间线上的一条记录（设计方案 6.3）：只记录本机发出和收到的内容，不在设备之间同步。
// Body 由 node 编码（文本、文件清单、保存位置、错误），在数据库里加密保存。
type Message struct {
	ID        int64
	Direction string
	PeerKey   string
	Status    string
	Files     int
	Texts     int
	Total     int64 // 文件总字节数
	Body      []byte
	CreatedAt time.Time
}

// AddMessage 记下一条历史，返回编号。CreatedAt 为零值时用当前时间。
func (s *Store) AddMessage(m Message) (int64, error) {
	defer s.changed(ChangeHistory)
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	res, err := s.db.Exec(`INSERT INTO messages (direction, peer_key, status, files, texts, total, body, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, m.Direction, m.PeerKey, m.Status, m.Files, m.Texts, m.Total, s.seal(fieldBody, m.Body), m.CreatedAt.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const messageColumns = `id, direction, peer_key, status, files, texts, total, body, created_at`

func (s *Store) scanMessage(row interface{ Scan(...any) error }) (*Message, error) {
	var m Message
	var body []byte
	var created int64
	if err := row.Scan(&m.ID, &m.Direction, &m.PeerKey, &m.Status, &m.Files, &m.Texts, &m.Total, &body, &created); err != nil {
		return nil, err
	}
	var err error
	if m.Body, err = s.open(fieldBody, body); err != nil {
		return nil, err
	}
	m.CreatedAt = time.UnixMilli(created)
	return &m, nil
}

// ListMessages 返回最近的 limit 条历史，新的在前；peerKey 不为空时只看和这台设备之间的。
func (s *Store) ListMessages(peerKey string, limit int) ([]Message, error) {
	q := `SELECT ` + messageColumns + ` FROM messages`
	var args []any
	if peerKey != "" {
		q += ` WHERE peer_key = ?`
		args = append(args, peerKey)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := s.scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// GetMessage 按编号读取一条历史；没有时返回 ErrNoMessage。
func (s *Store) GetMessage(id int64) (*Message, error) {
	m, err := s.scanMessage(s.db.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, id))
	if err != nil && isNoRows(err) {
		return nil, ErrNoMessage
	}
	return m, err
}

// DeleteMessage 删除一条历史；没有时返回 ErrNoMessage。
func (s *Store) DeleteMessage(id int64) error {
	defer s.changed(ChangeHistory)
	res, err := s.db.Exec(`DELETE FROM messages WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNoMessage
	}
	return nil
}

// ClearMessages 清空历史，返回删除的条数。
func (s *Store) ClearMessages() (int64, error) {
	defer s.changed(ChangeHistory)
	res, err := s.db.Exec(`DELETE FROM messages`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneMessages 删除 before 之前的历史（按天数自动清理）。
func (s *Store) PruneMessages(before time.Time) error {
	defer s.changed(ChangeHistory)
	_, err := s.db.Exec(`DELETE FROM messages WHERE created_at < ?`, before.UnixMilli())
	return err
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

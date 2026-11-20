package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// 传输相关的元数据键。
const (
	MetaSaveDir       = "save_dir"       // 接收文件的保存目录；为空时用默认目录
	MetaReceivePolicy = "receive_policy" // 接收策略："auto" | "ask" | "ask-above:<字节数>"
	MetaQueueDays     = "queue_days"     // 排队任务的保留天数；为空时用默认值
	MetaNotify        = "notify"         // 通知开关："off" 表示关闭，其他（包括没有设置）表示开启（M3b）
)

// 发送任务状态。
const (
	SendPending     = "pending"     // 排队：等对方上线
	SendActive      = "sending"     // 正在发送
	SendInterrupted = "interrupted" // 中断或出错，等下次重试（对方重新上线时立即重试）
	SendDone        = "done"
	SendFailed      = "failed"
	SendCancelled   = "cancelled"
)

// 待确认报价的状态。
const (
	InboxPending  = "pending"
	InboxAccepted = "accepted"
	InboxRejected = "rejected"
)

// ErrNotPending 表示待确认报价已经处理过或已过期。
var ErrNotPending = errors.New("store: offer is no longer pending")

// ErrTaskFinished 表示发送任务已经结束（或不存在），不能再暂停或继续。
var ErrTaskFinished = errors.New("store: outgoing task has finished")

// transferSchemaV1 已发布（迁移 1）：不要修改，新的表或列请在 migrations 末尾追加迁移。
const transferSchemaV1 = `
CREATE TABLE IF NOT EXISTS outgoing (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	peer_key    TEXT NOT NULL,
	transfer_id BLOB NOT NULL,
	offer       BLOB NOT NULL,
	sources     BLOB NOT NULL,
	status      TEXT NOT NULL,
	bytes_total INTEGER NOT NULL DEFAULT 0,
	bytes_done  INTEGER NOT NULL DEFAULT 0,
	cancel      INTEGER NOT NULL DEFAULT 0,
	error       TEXT NOT NULL DEFAULT '',
	updated_at  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS incoming_items (
	peer_key    TEXT NOT NULL,
	transfer_id TEXT NOT NULL,
	item        INTEGER NOT NULL,
	path        TEXT NOT NULL,
	done        INTEGER NOT NULL DEFAULT 0,
	digest      TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	PRIMARY KEY (peer_key, transfer_id, item)
);
CREATE TABLE IF NOT EXISTS inbox (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	peer_name  TEXT NOT NULL,
	summary    TEXT NOT NULL,
	total      INTEGER NOT NULL,
	status     TEXT NOT NULL,
	expires_at INTEGER NOT NULL
);
`

// Outgoing 是一个发送任务：命令行创建，守护进程执行，对方离线时一直排队到保留期限（设计方案 5.3）。
// 报价、本机路径和错误信息（可能含文件名）在数据库里加密保存（见 crypt.go）。
type Outgoing struct {
	ID         int64
	PeerKey    string
	TransferID []byte
	Offer      []byte // pb.Offer 的序列化字节
	Sources    []byte // 与报价条目对应的本机来源（由 node 编码）
	Status     string
	BytesTotal int64
	BytesDone  int64
	Cancel     bool // 命令行请求取消
	Error      string
	UpdatedAt  time.Time
	CreatedAt  time.Time
	Attempts   int       // 已经失败（中断或出错）的次数
	NextAt     time.Time // 最早什么时候再试；零值表示随时可以开始
	Paused     bool      // 用户暂停了任务：守护进程不开始发送，正在发送的那次停下来（M3b）
	ResumedAt  time.Time // 最近一次继续的时间；零值表示没有暂停过
}

// QueuedSince 返回排队期限的起点：创建时间和最近一次继续时间中较晚的那个（暂停期间不算排队）。
func (o Outgoing) QueuedSince() time.Time {
	if o.ResumedAt.After(o.CreatedAt) {
		return o.ResumedAt
	}
	return o.CreatedAt
}

// CreateOutgoing 创建一个等待发送的任务，返回任务 ID。
func (s *Store) CreateOutgoing(peerKey string, transferID, offer, sources []byte, total int64) (int64, error) {
	defer s.changed(ChangeTransfers)
	now := time.Now().UnixMilli()
	res, err := s.db.Exec(`INSERT INTO outgoing (peer_key, transfer_id, offer, sources, status, bytes_total, updated_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, peerKey, transferID, s.seal(fieldOffer, offer), s.seal(fieldSources, sources), SendPending, total, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const outgoingColumns = `id, peer_key, transfer_id, offer, sources, status, bytes_total, bytes_done, cancel, error, updated_at, created_at, attempts, next_at, paused, resumed_at`

func (s *Store) scanOutgoing(row interface{ Scan(...any) error }) (*Outgoing, error) {
	var o Outgoing
	var cancel, paused int
	var updated, created, next, resumed int64
	var offer, sources, errMsg []byte
	if err := row.Scan(&o.ID, &o.PeerKey, &o.TransferID, &offer, &sources, &o.Status, &o.BytesTotal, &o.BytesDone, &cancel, &errMsg, &updated, &created, &o.Attempts, &next, &paused, &resumed); err != nil {
		return nil, err
	}
	var err error
	if o.Offer, err = s.open(fieldOffer, offer); err != nil {
		return nil, err
	}
	if o.Sources, err = s.open(fieldSources, sources); err != nil {
		return nil, err
	}
	if o.Error, err = s.openString(fieldError, errMsg); err != nil {
		return nil, err
	}
	o.Cancel = cancel == 1
	o.Paused = paused == 1
	if resumed != 0 {
		o.ResumedAt = time.UnixMilli(resumed)
	}
	o.UpdatedAt = time.UnixMilli(updated)
	if created == 0 {
		// 旧版本写入的行没有这一列（迁移会补上，但防一手遗漏）：退回用 updated_at，
		// 不要让排队有效期从 1970 年算起。
		created = updated
	}
	o.CreatedAt = time.UnixMilli(created)
	if next != 0 {
		o.NextAt = time.UnixMilli(next)
	}
	return &o, nil
}

func (s *Store) scanOutgoings(rows *sql.Rows) ([]Outgoing, error) {
	defer rows.Close()
	var out []Outgoing
	for rows.Next() {
		o, err := s.scanOutgoing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// GetOutgoing 按 ID 读取发送任务。
func (s *Store) GetOutgoing(id int64) (*Outgoing, error) {
	return s.scanOutgoing(s.db.QueryRow(`SELECT `+outgoingColumns+` FROM outgoing WHERE id = ?`, id))
}

// OpenOutgoing 返回所有尚未结束的发送任务（等待、进行中、中断）。
func (s *Store) OpenOutgoing() ([]Outgoing, error) {
	rows, err := s.db.Query(`SELECT `+outgoingColumns+` FROM outgoing WHERE status IN (?, ?, ?) ORDER BY id`,
		SendPending, SendActive, SendInterrupted)
	if err != nil {
		return nil, err
	}
	return s.scanOutgoings(rows)
}

// ListOutgoing 返回全部发送任务（含已结束、尚未清理的），按创建顺序排列。
func (s *Store) ListOutgoing() ([]Outgoing, error) {
	rows, err := s.db.Query(`SELECT ` + outgoingColumns + ` FROM outgoing ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return s.scanOutgoings(rows)
}

// RetryOutgoing 记录一次失败（中断或出错）：任务回到中断状态，next 之后再试；next 为零值表示随时可以重试。
func (s *Store) RetryOutgoing(id int64, attempts int, next time.Time, errMsg string) error {
	defer s.changed(ChangeTransfers)
	var nextAt int64
	if !next.IsZero() {
		nextAt = next.UnixMilli()
	}
	_, err := s.db.Exec(`UPDATE outgoing SET status = ?, attempts = ?, next_at = ?, error = ?, updated_at = ? WHERE id = ?`,
		SendInterrupted, attempts, nextAt, s.sealString(fieldError, errMsg), time.Now().UnixMilli(), id)
	return err
}

// RetryNow 让发给 peerKey 的排队和中断任务立即可以重试（对方刚刚连上时调用）。
func (s *Store) RetryNow(peerKey string) error {
	defer s.changed(ChangeTransfers)
	_, err := s.db.Exec(`UPDATE outgoing SET next_at = 0 WHERE peer_key = ? AND status IN (?, ?)`,
		peerKey, SendPending, SendInterrupted)
	return err
}

// RetryOutgoingNow 让一个排队或中断、没有暂停也没有取消的任务立即可以重试（用户手动重试）。
// 只清零 next_at：正在发送的任务（status 为 sending）不受影响，守护进程用 sending 表保证同一任务不会同时发两次。
// 没有命中任何行时返回 ErrTaskFinished，调用方再读一次任务说明原因。
func (s *Store) RetryOutgoingNow(id int64) error {
	defer s.changed(ChangeTransfers)
	res, err := s.db.Exec(`UPDATE outgoing SET next_at = 0 WHERE id = ? AND status IN (?, ?) AND paused = 0 AND cancel = 0`,
		id, SendPending, SendInterrupted)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrTaskFinished
	}
	return nil
}

// ReplaceOffer 换成新的报价（源文件被修改后重新报价，需要新的传输 ID）。
func (s *Store) ReplaceOffer(id int64, transferID, offer []byte, total int64) error {
	defer s.changed(ChangeTransfers)
	_, err := s.db.Exec(`UPDATE outgoing SET transfer_id = ?, offer = ?, bytes_total = ?, bytes_done = 0, updated_at = ? WHERE id = ?`,
		transferID, s.seal(fieldOffer, offer), total, time.Now().UnixMilli(), id)
	return err
}

// UpdateOutgoing 设置任务状态和错误信息。
func (s *Store) UpdateOutgoing(id int64, status, errMsg string) error {
	defer s.changed(ChangeTransfers)
	_, err := s.db.Exec(`UPDATE outgoing SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, s.sealString(fieldError, errMsg), time.Now().UnixMilli(), id)
	return err
}

// SetOutgoingProgress 记录已发送（含续传跳过）的字节数。
func (s *Store) SetOutgoingProgress(id, done int64) error {
	defer s.changed(ChangeTransfers)
	_, err := s.db.Exec(`UPDATE outgoing SET bytes_done = ?, updated_at = ? WHERE id = ?`, done, time.Now().UnixMilli(), id)
	return err
}

// CancelOutgoing 请求取消任务；守护进程在下次检查时停止发送。
func (s *Store) CancelOutgoing(id int64) error {
	defer s.changed(ChangeTransfers)
	_, err := s.db.Exec(`UPDATE outgoing SET cancel = 1 WHERE id = ?`, id)
	return err
}

// PauseOutgoing 暂停一个还没结束的任务：守护进程不再开始发送它，正在发送的那次在下次检查时停下（保留续传进度）。
func (s *Store) PauseOutgoing(id int64) error {
	defer s.changed(ChangeTransfers)
	return s.updateOpen(`UPDATE outgoing SET paused = 1 WHERE id = ? AND status IN (?, ?, ?)`, id)
}

// ResumeOutgoing 继续一个暂停的任务：立即可以发送，排队期限从现在重新计算。
func (s *Store) ResumeOutgoing(id int64) error {
	defer s.changed(ChangeTransfers)
	return s.updateOpen(`UPDATE outgoing SET paused = 0, next_at = 0, resumed_at = ? WHERE id = ? AND status IN (?, ?, ?)`,
		time.Now().UnixMilli(), id)
}

// updateOpen 执行只对未结束任务生效的更新（args 之后自动补上三个未结束状态）；没有命中任何行时返回 ErrTaskFinished。
func (s *Store) updateOpen(query string, args ...any) error {
	res, err := s.db.Exec(query, append(args, SendPending, SendActive, SendInterrupted)...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrTaskFinished
	}
	return nil
}

// DeleteOutgoing 删除任务。
func (s *Store) DeleteOutgoing(id int64) error {
	defer s.changed(ChangeTransfers)
	_, err := s.db.Exec(`DELETE FROM outgoing WHERE id = ?`, id)
	return err
}

// RecoverOutgoing 在守护进程启动时调用：上次退出时还在发送的任务改为中断，等对方上线后续传。
func (s *Store) RecoverOutgoing() error {
	defer s.changed(ChangeTransfers)
	_, err := s.db.Exec(`UPDATE outgoing SET status = ?, updated_at = ? WHERE status = ?`,
		SendInterrupted, time.Now().UnixMilli(), SendActive)
	return err
}

// PruneOutgoing 删除 before 之前就已结束的任务（排队后命令行已经退出的，或命令行被强行结束的）。
func (s *Store) PruneOutgoing(before time.Time) error {
	defer s.changed(ChangeTransfers)
	_, err := s.db.Exec(`DELETE FROM outgoing WHERE status IN (?, ?, ?) AND updated_at < ?`,
		SendDone, SendFailed, SendCancelled, before.UnixMilli())
	return err
}

// IncomingItem 是一次接收中某个条目的续传记录。
type IncomingItem struct {
	Path   string // 保存目录下实际使用的相对路径（已清理、已去重）
	Done   bool
	Digest string // 报价条目的摘要，续传时用来确认是同一份报价
}

// IncomingKey 标识一次接收：发送方 KeyID 加传输 ID（十六进制）。
type IncomingKey struct {
	PeerKey    string
	TransferID string
}

// IncomingItems 返回一次接收的续传记录；没有记录（全新的传输）时返回空 map。
func (s *Store) IncomingItems(k IncomingKey) (map[int]IncomingItem, error) {
	rows, err := s.db.Query(`SELECT item, path, done, digest FROM incoming_items WHERE peer_key = ? AND transfer_id = ?`, k.PeerKey, k.TransferID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]IncomingItem{}
	for rows.Next() {
		var i, done int
		var it IncomingItem
		var p []byte
		if err := rows.Scan(&i, &p, &done, &it.Digest); err != nil {
			return nil, err
		}
		if it.Path, err = s.openString(fieldPath, p); err != nil {
			return nil, err
		}
		it.Done = done == 1
		out[i] = it
	}
	return out, rows.Err()
}

// AddIncoming 记录一次新接收的全部条目及其实际路径，以及报价条目的摘要。
func (s *Store) AddIncoming(k IncomingKey, digest string, paths map[int]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for i, p := range paths {
		if _, err := tx.Exec(`INSERT INTO incoming_items (peer_key, transfer_id, item, path, digest, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			k.PeerKey, k.TransferID, i, s.sealString(fieldPath, p), digest, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkIncomingDone 标记一个条目已完整收到。
func (s *Store) MarkIncomingDone(k IncomingKey, item int) error {
	_, err := s.db.Exec(`UPDATE incoming_items SET done = 1 WHERE peer_key = ? AND transfer_id = ? AND item = ?`, k.PeerKey, k.TransferID, item)
	return err
}

// DeleteIncoming 删除一次接收的全部续传记录（传输结束后，文件名不再留在数据库里）。
func (s *Store) DeleteIncoming(k IncomingKey) error {
	_, err := s.db.Exec(`DELETE FROM incoming_items WHERE peer_key = ? AND transfer_id = ?`, k.PeerKey, k.TransferID)
	return err
}

// IncomingTops 返回所有尚未结束的接收（进行中或中断）占用的顶层名字。新的接收不能再用这些名字，
// 否则两个传输会写进同一个文件夹，或者争同一个文件名。
func (s *Store) IncomingTops() ([]string, error) {
	// 路径是密文（每次加密的结果都不同），只能读出来解密后在 Go 里去重。
	rows, err := s.db.Query(`SELECT path FROM incoming_items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var v []byte
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		p, err := s.openString(fieldPath, v)
		if err != nil {
			return nil, err
		}
		if p == "" {
			continue
		}
		top, _, _ := strings.Cut(p, "/")
		if !seen[top] {
			seen[top] = true
			out = append(out, top)
		}
	}
	return out, rows.Err()
}

// IncomingKeys 返回某个发送方当前全部未结束接收的传输 ID（进行中或中断，不管新旧）。
func (s *Store) IncomingKeys(peerKey string) ([]IncomingKey, error) {
	rows, err := s.db.Query(`SELECT DISTINCT transfer_id FROM incoming_items WHERE peer_key = ?`, peerKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IncomingKey
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, IncomingKey{PeerKey: peerKey, TransferID: id})
	}
	return out, rows.Err()
}

// StaleIncoming 返回 before 之前开始、至今没有结束的接收。
func (s *Store) StaleIncoming(before time.Time) ([]IncomingKey, error) {
	rows, err := s.db.Query(`SELECT DISTINCT peer_key, transfer_id FROM incoming_items WHERE created_at < ?`, before.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IncomingKey
	for rows.Next() {
		var k IncomingKey
		if err := rows.Scan(&k.PeerKey, &k.TransferID); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// InboxEntry 是一个等待用户确认的报价（"总是询问"或超过大小阈值时）。
type InboxEntry struct {
	ID        int64
	PeerName  string
	Summary   string // 内容摘要，例如"照片（3 个文件）"
	Total     int64  // 总字节数
	Status    string
	ExpiresAt time.Time
}

// CreateInbox 记录一个待确认的报价，返回编号。
func (s *Store) CreateInbox(peerName, summary string, total int64, ttl time.Duration) (int64, error) {
	defer s.changed(ChangeInbox)
	res, err := s.db.Exec(`INSERT INTO inbox (peer_name, summary, total, status, expires_at) VALUES (?, ?, ?, ?, ?)`,
		peerName, s.sealString(fieldSummary, summary), total, InboxPending, time.Now().Add(ttl).UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const inboxColumns = `id, peer_name, summary, total, status, expires_at`

func (s *Store) scanInbox(row interface{ Scan(...any) error }) (*InboxEntry, error) {
	var e InboxEntry
	var expires int64
	var summary []byte
	if err := row.Scan(&e.ID, &e.PeerName, &summary, &e.Total, &e.Status, &expires); err != nil {
		return nil, err
	}
	var err error
	if e.Summary, err = s.openString(fieldSummary, summary); err != nil {
		return nil, err
	}
	e.ExpiresAt = time.UnixMilli(expires)
	return &e, nil
}

// GetInbox 按编号读取待确认报价。
func (s *Store) GetInbox(id int64) (*InboxEntry, error) {
	return s.scanInbox(s.db.QueryRow(`SELECT `+inboxColumns+` FROM inbox WHERE id = ?`, id))
}

// PendingInbox 返回尚未处理且未过期的报价。
func (s *Store) PendingInbox(now time.Time) ([]InboxEntry, error) {
	rows, err := s.db.Query(`SELECT `+inboxColumns+` FROM inbox WHERE status = ? AND expires_at > ? ORDER BY id`, InboxPending, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxEntry
	for rows.Next() {
		e, err := s.scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// DecideInbox 接受或拒绝一个待确认报价；已处理或已过期时返回 ErrNotPending。
func (s *Store) DecideInbox(id int64, accept bool) error {
	defer s.changed(ChangeInbox)
	status := InboxRejected
	if accept {
		status = InboxAccepted
	}
	res, err := s.db.Exec(`UPDATE inbox SET status = ? WHERE id = ? AND status = ? AND expires_at > ?`,
		status, id, InboxPending, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotPending
	}
	return nil
}

// DeleteInbox 删除一个报价记录（守护进程拿到结果后调用，摘要不再留在数据库里）。
func (s *Store) DeleteInbox(id int64) error {
	defer s.changed(ChangeInbox)
	_, err := s.db.Exec(`DELETE FROM inbox WHERE id = ?`, id)
	return err
}

// ClearInbox 删除所有待确认报价。守护进程启动时调用：留下来的记录都属于已经退出的守护进程
// （它们本该在 decideOffer 返回时被 DeleteInbox 删掉，但守护进程可能在询问用户期间就退出了）。
func (s *Store) ClearInbox() error {
	defer s.changed(ChangeInbox)
	_, err := s.db.Exec(`DELETE FROM inbox`)
	return err
}

// AddStoppedIncoming 记住本机停止了这次接收：对方之后再发同一个报价（续传时传输 ID 不变）会被直接拒绝。
func (s *Store) AddStoppedIncoming(k IncomingKey) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO stopped_incoming (peer_key, transfer_id, created_at) VALUES (?, ?, ?)`,
		k.PeerKey, k.TransferID, time.Now().UnixMilli())
	return err
}

// IsStoppedIncoming 报告本机是否停止过这次接收。
func (s *Store) IsStoppedIncoming(k IncomingKey) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM stopped_incoming WHERE peer_key = ? AND transfer_id = ?`, k.PeerKey, k.TransferID).Scan(&n)
	return n > 0, err
}

// PruneStoppedIncoming 删除 before 之前记下的停止记录（发送方早就放弃了那次传输）。
func (s *Store) PruneStoppedIncoming(before time.Time) error {
	_, err := s.db.Exec(`DELETE FROM stopped_incoming WHERE created_at < ?`, before.UnixMilli())
	return err
}

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// CircleMove 是本机参与合并之后的迁移记录（合并设备圈设计 8.2），最多一条。
type CircleMove struct {
	OldCircle string    // 来源圈的圈子 ID
	OldChain  [][]byte  // 本机见过的最新的来源圈签名链；到期后清空（nil）
	Brought   []string  // 合并时（和之后核对时）从来源圈带进新圈的设备 KeyID
	Pending   []string  // 还没跟过来的设备 KeyID
	Elsewhere []string  // 回复"已经去了别的设备圈"的设备 KeyID（还在 Pending 里）
	ExpiresAt time.Time // 迁移等待期的结束时间
}

func joinIDs(ids []string) string { return strings.Join(ids, ",") }

// splitIDs 把逗号连接的 KeyID 列表还原；空串得到 nil，而不是 [""]。
func splitIDs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// CircleMove 返回迁移记录；没有时返回 nil, nil。
func (s *Store) CircleMove() (*CircleMove, error) {
	var m CircleMove
	var chain []byte
	var brought, pending, elsewhere string
	var expires int64
	err := s.db.QueryRow(`SELECT old_circle, old_chain, brought, pending, elsewhere, expires_at FROM circle_moves WHERE id = 1`).
		Scan(&m.OldCircle, &chain, &brought, &pending, &elsewhere, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if chain != nil {
		if err := json.Unmarshal(chain, &m.OldChain); err != nil {
			return nil, err
		}
	}
	m.Brought, m.Pending, m.Elsewhere = splitIDs(brought), splitIDs(pending), splitIDs(elsewhere)
	m.ExpiresAt = time.Unix(expires, 0)
	return &m, nil
}

// SaveCircleMove 整条替换迁移记录。
func (s *Store) SaveCircleMove(m *CircleMove) error {
	var chain []byte // nil 存 NULL
	if m.OldChain != nil {
		var err error
		if chain, err = json.Marshal(m.OldChain); err != nil {
			return err
		}
	}
	defer s.changed(ChangeDevices)
	_, err := s.db.Exec(`INSERT OR REPLACE INTO circle_moves (id, old_circle, old_chain, brought, pending, elsewhere, expires_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)`,
		m.OldCircle, chain, joinIDs(m.Brought), joinIDs(m.Pending), joinIDs(m.Elsewhere), m.ExpiresAt.Unix())
	return err
}

// DeleteCircleMove 删除迁移记录（没有记录时什么也不做）。
func (s *Store) DeleteCircleMove() error {
	defer s.changed(ChangeDevices)
	_, err := s.db.Exec(`DELETE FROM circle_moves`)
	return err
}

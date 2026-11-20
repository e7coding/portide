package node

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"portide/core/i18n"
	"portide/core/pb"
	"portide/core/store"
	"portide/core/transfer"
)

// 本地历史（设计方案 6.3）：只有一条时间线，记录本机发出和收到的内容，不在设备之间同步。
// 正文（文本、文件清单、保存位置、错误）在数据库里加密保存。

// HistoryBody 是一条历史的正文。
type HistoryBody struct {
	PeerName string        `json:"peer"`
	Texts    []string      `json:"texts,omitempty"`
	Files    []HistoryFile `json:"files,omitempty"`
	Dir      string        `json:"dir,omitempty"`   // 收到：保存目录
	Error    string        `json:"error,omitempty"` // 没有完全送达时的原因
	// Summary 是报价的摘要（transfer.Describe，例如"季度报告.pdf（2.1 MB）"），只在接收没有完成（停止、取消、部分收到）时记下：
	// 这时 Files 里只有真正收到的文件，可能一个也没有，时间线靠它说明这次接收的是什么（M3c）。
	Summary string `json:"summary,omitempty"`
}

// HistoryFile 是历史里的一个文件。
type HistoryFile struct {
	Path  string `json:"path"`            // 收到：保存目录下的相对路径；发出：报价里的相对路径
	Size  uint64 `json:"size"`            // 字节数
	Local string `json:"local,omitempty"` // 发出：本机上的原文件
}

// HistoryEntry 是读出来的一条历史。
type HistoryEntry struct {
	store.Message
	Body HistoryBody
}

// LocalPath 返回历史里一个文件在本机上的位置：收到的在保存目录里，发出的是原文件。
func (e HistoryEntry) LocalPath(f HistoryFile) string {
	if e.Direction == store.MessageOut {
		if f.Local != "" {
			return f.Local
		}
		return f.Path // 内存中的来源（超长文本）没有本机文件
	}
	return filepath.Join(e.Body.Dir, filepath.FromSlash(f.Path))
}

// History 返回最近的 limit 条历史（新的在前）；peerKey 不为空时只看和这台设备之间的。
func (n *Node) History(peerKey string, limit int) ([]HistoryEntry, error) {
	msgs, err := n.store.ListMessages(peerKey, limit)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, 0, len(msgs))
	for _, m := range msgs {
		e, err := decodeHistory(m)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// HistoryEntry 按编号读取一条历史；没有时返回 store.ErrNoMessage。
func (n *Node) HistoryEntry(id int64) (*HistoryEntry, error) {
	m, err := n.store.GetMessage(id)
	if err != nil {
		return nil, err
	}
	e, err := decodeHistory(*m)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func decodeHistory(m store.Message) (HistoryEntry, error) {
	e := HistoryEntry{Message: m}
	if err := json.Unmarshal(m.Body, &e.Body); err != nil {
		return e, fmt.Errorf("node: history entry %d is corrupt: %w", m.ID, err)
	}
	e.Message.Body = nil
	return e, nil
}

// DefaultHistoryDays 表示默认不自动清理历史。
const DefaultHistoryDays = 0

// HistoryDays 返回历史的自动清理天数；0 表示永久保留。
func (n *Node) HistoryDays() int {
	v, _, _ := n.store.GetMeta(store.MetaHistoryDays)
	days, err := strconv.Atoi(v)
	if err != nil || days < 0 {
		return DefaultHistoryDays
	}
	return days
}

// SetHistoryDays 设置历史的自动清理天数（0 表示永久保留，最多 3650 天）。
func (n *Node) SetHistoryDays(days int) error {
	if days < 0 || days > 3650 {
		return fmt.Errorf("node: history days must be between 0 and 3650, got %d", days)
	}
	return n.store.SetMeta(store.MetaHistoryDays, strconv.Itoa(days))
}

// pruneHistory 按天数自动清理历史（守护进程定期调用）。
func (n *Node) pruneHistory(now time.Time) {
	if days := n.HistoryDays(); days > 0 {
		_ = n.store.PruneMessages(now.Add(-time.Duration(days) * 24 * time.Hour))
	}
}

// addHistory 记下一条历史；写入失败只记日志，不影响传输本身。
func (n *Node) addHistory(direction, peerKey, status string, body HistoryBody) {
	m := store.Message{Direction: direction, PeerKey: peerKey, Status: status, Texts: len(body.Texts)}
	for _, f := range body.Files {
		m.Files++
		m.Total += int64(f.Size)
	}
	b, err := json.Marshal(body)
	if err == nil {
		m.Body = b
		var id int64
		if id, err = n.store.AddMessage(m); err == nil {
			n.historyEvent(id, direction, status, body)
		}
	}
	if err != nil {
		n.opts.Logf(i18n.T("写入历史记录失败：%v"), err)
	}
}

// logDescribe 是写进日志的内容摘要。transfer.Describe 对纯文本报价会带上文本开头，
// 而文本内容只能进加密的历史、不能进日志，所以纯文本报价只写"N 段文本"。
func logDescribe(o *pb.Offer) string {
	for _, it := range o.Items {
		if it.Kind != pb.ItemKind_ITEM_KIND_TEXT {
			desc, _ := transfer.Describe(o)
			return desc
		}
	}
	return i18n.Tf("%d 段文本", len(o.Items))
}

// offerTexts 返回报价里的文本条目。
func offerTexts(o *pb.Offer) []string {
	var out []string
	for _, it := range o.Items {
		if it.Kind == pb.ItemKind_ITEM_KIND_TEXT {
			out = append(out, it.Text)
		}
	}
	return out
}

// recordReceive 记下一次结束的接收；中断（等续传）的不记，等它最终结束时再记。
func (n *Node) recordReceive(peer string, o *pb.Offer, r transfer.Result) {
	var status string
	switch r.End {
	case transfer.EndComplete:
		status = store.MessageDone
	case transfer.EndPartial:
		status = store.MessagePartial
	case transfer.EndCancelled, transfer.EndStopped:
		status = store.MessageCancelled
	default:
		return
	}
	body := HistoryBody{PeerName: n.memberName(peer), Texts: offerTexts(o), Dir: r.SaveDir}
	if r.End == transfer.EndStopped {
		body.Error = i18n.T("已停止接收")
	}
	if r.End != transfer.EndComplete {
		body.Summary, _ = transfer.Describe(o)
	}
	for _, f := range r.Saved {
		body.Files = append(body.Files, HistoryFile{Path: f.Path, Size: f.Size})
	}
	n.addHistory(store.MessageIn, peer, status, body)
}

// recordSend 记下一次结束的发送（送达、部分送达、失败或取消）；errMsg 是给用户看的原因。
func (n *Node) recordSend(peerKey string, o *pb.Offer, srcs []transfer.Source, status, errMsg string) {
	body := HistoryBody{PeerName: n.memberName(peerKey), Texts: offerTexts(o), Error: errMsg}
	for i, it := range o.Items {
		if it.Kind != pb.ItemKind_ITEM_KIND_FILE {
			continue
		}
		f := HistoryFile{Path: it.Path, Size: it.Size}
		if i < len(srcs) {
			f.Local = srcs[i].Path
		}
		body.Files = append(body.Files, f)
	}
	n.addHistory(store.MessageOut, peerKey, status, body)
}

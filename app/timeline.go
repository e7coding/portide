package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"portide/core/i18n"
	"portide/core/node"
	"portide/core/pb"
	"portide/core/store"
	"portide/core/transfer"
)

// 时间线条目的种类。
const (
	KindMessage = "message" // 一条历史（已经结束的收发）
	KindTask    = "task"    // 还没结束的发送任务（排队、发送中、中断后等待重试）
)

// TimelineFilesShown 是一条历史在时间线里最多带几个文件：多文件 / 文件夹时折叠显示（M3a 设计 3.1），
// 不然一个上万个文件的文件夹每次刷新都要检查上万个文件、在界面上画上万行。
const TimelineFilesShown = 5

// TimelineFile 是时间线里的一个文件。
type TimelineFile struct {
	Name   string `json:"name"` // 相对路径（文件夹里的文件带目录）
	Size   uint64 `json:"size"`
	Exists bool   `json:"exists"` // 本机上还能找到这个文件（收到的文件可能已被移走）
}

// TimelineItem 是时间线上的一条。
type TimelineItem struct {
	Key        string         `json:"key"` // 界面列表用的唯一键："m<编号>" 或 "t<编号>"
	Kind       string         `json:"kind"`
	ID         int64          `json:"id"`        // 历史编号或任务编号
	Direction  string         `json:"direction"` // "in" | "out"
	PeerKey    string         `json:"peerKey"`
	PeerName   string         `json:"peerName"`
	Status     string         `json:"status"` // 历史：done / partial / failed / cancelled；任务：pending / sending / interrupted
	Paused     bool           `json:"paused"` // 任务：用户暂停了它
	Texts      []string       `json:"texts"`
	Files      []TimelineFile `json:"files"`     // 最多 TimelineFilesShown 个（这条历史里的前几个文件）
	FileCount  int            `json:"fileCount"` // 这条历史里的文件总数
	Summary    string         `json:"summary"`   // 任务的内容摘要，例如"报告.pdf 等 3 个文件"；没有完成的接收也带它（M3c）
	BytesDone  int64          `json:"bytesDone"`
	BytesTotal int64          `json:"bytesTotal"`
	Error      string         `json:"error"`
	At         int64          `json:"at"` // Unix 毫秒
}

// SendResult 是一次发送的结果。
type SendResult struct {
	Tasks   int      `json:"tasks"`   // 建了几个发送任务（每个目标设备一个）
	Skipped []string `json:"skipped"` // 跳过的路径（符号链接、特殊文件或无法读取）
}

// InboxItem 是一条等待确认的接收。
type InboxItem struct {
	ID        int64  `json:"id"`
	PeerName  string `json:"peerName"`
	Summary   string `json:"summary"`
	Total     int64  `json:"total"`
	ExpiresAt int64  `json:"expiresAt"` // Unix 毫秒
}

// Timeline 返回最近 limit 条历史和全部未结束的发送任务，按时间从旧到新排列。peerKey 不为空时只看和这台设备之间的。
func (s *Service) Timeline(peerKey string, limit int) ([]TimelineItem, error) {
	n, err := s.node()
	if err != nil {
		return nil, err
	}
	entries, err := n.History(peerKey, limit)
	if err != nil {
		return nil, uiError(err)
	}
	names := deviceNames(n)
	items := make([]TimelineItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, s.messageItem(e, names))
	}
	tasks, err := n.Store().ListOutgoing()
	if err != nil {
		return nil, uiError(err)
	}
	for _, t := range tasks {
		if (peerKey != "" && t.PeerKey != peerKey) || !unfinished(t.Status) {
			continue
		}
		items = append(items, taskItem(t, names[t.PeerKey]))
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].At < items[j].At })
	return items, nil
}

func unfinished(status string) bool {
	return status == store.SendPending || status == store.SendActive || status == store.SendInterrupted
}

// localPath 返回历史里一个文件在本机上的位置；设置了 Config.SaveDir 时，收到的文件按它定位（M4）。
func (s *Service) localPath(e node.HistoryEntry, f node.HistoryFile) string {
	if s.cfg.SaveDir != "" && e.Direction == store.MessageIn {
		return filepath.Join(s.cfg.SaveDir, filepath.FromSlash(f.Path))
	}
	return e.LocalPath(f)
}

// messageItem 把一条历史换成时间线条目；names 是设备圈成员现在的名字（对方改过名时显示新名字）。
func (s *Service) messageItem(e node.HistoryEntry, names map[string]string) TimelineItem {
	it := TimelineItem{
		Key: fmt.Sprintf("m%d", e.ID), Kind: KindMessage, ID: e.ID, Direction: e.Direction,
		PeerKey: e.PeerKey, PeerName: currentName(names, e.PeerKey, e.Body.PeerName), Status: e.Status,
		Texts: e.Body.Texts, Files: []TimelineFile{}, BytesTotal: e.Total, BytesDone: e.Total,
		Summary: e.Body.Summary, Error: e.Body.Error, At: e.CreatedAt.UnixMilli(),
	}
	if it.Texts == nil {
		it.Texts = []string{}
	}
	it.FileCount = len(e.Body.Files)
	shown := e.Body.Files
	if len(shown) > TimelineFilesShown {
		shown = shown[:TimelineFilesShown] // 序号和完整列表一致：OpenFile / RevealFile 照常按序号找
	}
	for _, f := range shown {
		_, statErr := os.Stat(s.localPath(e, f))
		it.Files = append(it.Files, TimelineFile{Name: f.Path, Size: f.Size, Exists: statErr == nil})
	}
	return it
}

func taskItem(t store.Outgoing, peerName string) TimelineItem {
	summary := ""
	var o pb.Offer
	if proto.Unmarshal(t.Offer, &o) == nil {
		summary, _ = transfer.Describe(&o)
	}
	return TimelineItem{
		Key: fmt.Sprintf("t%d", t.ID), Kind: KindTask, ID: t.ID, Direction: store.MessageOut,
		PeerKey: t.PeerKey, PeerName: peerName, Status: t.Status, Paused: t.Paused, Texts: []string{}, Files: []TimelineFile{},
		Summary: summary, BytesDone: t.BytesDone, BytesTotal: t.BytesTotal, Error: t.Error, At: t.CreatedAt.UnixMilli(),
	}
}

// currentName 返回 key 这台设备现在的名字；它已经不在设备圈里时用历史里记下的 stored。
func currentName(names map[string]string, key, stored string) string {
	if name, ok := names[key]; ok {
		return name
	}
	return stored
}

func deviceNames(n *node.Node) map[string]string {
	names := map[string]string{}
	if devs, err := n.Devices(); err == nil {
		for _, d := range devs {
			names[d.KeyID] = d.Name
		}
	}
	return names
}

// SendText 把一段文本发给 targets（KeyID 列表）里的每台设备。
func (s *Service) SendText(targets []string, text string) (SendResult, error) {
	if text == "" {
		return SendResult{}, i18n.New("没有要发送的内容")
	}
	var p transfer.Plan
	if err := p.AddText(text, time.Now()); err != nil {
		return SendResult{}, uiError(err)
	}
	return s.send(targets, &p)
}

// SendFiles 把文件和文件夹发给 targets 里的每台设备。
func (s *Service) SendFiles(targets []string, paths []string) (SendResult, error) {
	var p transfer.Plan
	for _, path := range paths {
		if err := p.AddPath(path); errors.Is(err, transfer.ErrTooManyItems) {
			return SendResult{}, i18n.New("一次最多发送 %d 个条目，请分批发送或先打包", transfer.MaxItems)
		} else if err != nil {
			return SendResult{}, i18n.New("无法发送 %s：%v", path, err)
		}
	}
	if len(p.Items) == 0 {
		return SendResult{Skipped: nonNil(p.Skipped)}, i18n.New("没有可以发送的文件")
	}
	return s.send(targets, &p)
}

// send 给每个目标建一个任务（设计方案 5.3：互不影响）。中途出错时撤销这次已经建好的任务。
func (s *Service) send(targets []string, p *transfer.Plan) (SendResult, error) {
	n, err := s.node()
	if err != nil {
		return SendResult{}, err
	}
	if len(targets) == 0 {
		return SendResult{}, i18n.New("请选择要发给哪台设备")
	}
	members := map[string]bool{}
	devs, err := n.Devices()
	if err != nil {
		return SendResult{}, uiError(err)
	}
	for _, d := range devs {
		if !d.Self {
			members[d.KeyID] = true
		}
	}
	seen := map[string]bool{}
	var ids []int64
	for _, target := range targets {
		if seen[target] {
			continue
		}
		seen[target] = true
		if !members[target] {
			return SendResult{}, s.undo(n, ids, i18n.New("目标设备不在设备圈里"))
		}
		id, err := n.QueueSend(target, p)
		if errors.Is(err, transfer.ErrBadOffer) {
			err = i18n.New("有文件名或路径太长、或目录层级太深，对方无法接收")
		}
		if err != nil {
			return SendResult{}, s.undo(n, ids, uiError(err))
		}
		ids = append(ids, id)
	}
	return SendResult{Tasks: len(ids), Skipped: nonNil(p.Skipped)}, nil
}

func (s *Service) undo(n *node.Node, ids []int64, err error) error {
	for _, id := range ids {
		_ = n.Store().DeleteOutgoing(id) // 守护进程把被删除的任务当作取消
	}
	return err
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// pasteMaxBytes 是一次粘贴的图片的大小上限。
const pasteMaxBytes = 64 << 20

// SendPasted 把粘贴板里的图片（base64，可以带 "data:…;base64," 前缀）存成名为 name 的文件发给 targets。
// 文件先写进数据目录下的 pasted/<随机目录>/，不再被未结束的任务引用后，下一次粘贴时清理（M3c 设计 5.4）。
func (s *Service) SendPasted(targets []string, name, base64Data string) (SendResult, error) {
	if _, data, ok := strings.Cut(base64Data, ";base64,"); ok {
		base64Data = data
	}
	if base64.StdEncoding.DecodedLen(len(base64Data)) > pasteMaxBytes {
		return SendResult{}, i18n.New("粘贴的内容太大，请先保存成文件再发送")
	}
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil || len(data) == 0 {
		return SendResult{}, i18n.New("无法读取粘贴的内容")
	}
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == ".." || name == string(filepath.Separator) || name == "" {
		name = i18n.T("粘贴的图片") + ".png"
	}
	dir := s.pasteDir()
	// 清理、写入和建任务要串行：Wails 会并发调用绑定方法，两次粘贴同时进行时，
	// 后一次的清理可能正好删掉前一次刚写进去、还没建上任务的文件（M3c 复审修复）。
	s.paste.Lock()
	defer s.paste.Unlock()
	_ = CleanOutbox(s, dir) // 尽力清理：失败不影响这次发送
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return SendResult{}, i18n.New("无法保存粘贴的内容：%v", err)
	}
	sub, err := os.MkdirTemp(dir, "")
	if err != nil {
		return SendResult{}, i18n.New("无法保存粘贴的内容：%v", err)
	}
	path := filepath.Join(sub, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(sub)
		return SendResult{}, i18n.New("无法保存粘贴的内容：%v", err)
	}
	res, err := s.SendFiles(targets, []string{path})
	if err != nil {
		_ = os.RemoveAll(sub)
	}
	return res, err
}

func (s *Service) pasteDir() string { return filepath.Join(s.cfg.DataDir, "pasted") }

// DeleteHistory 删除本机的一条历史记录（不删文件，也不影响对方的记录）。
func (s *Service) DeleteHistory(id int64) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	return uiError(n.Store().DeleteMessage(id))
}

// CancelSend 取消一个还没结束的发送任务。
func (s *Service) CancelSend(id int64) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	t, err := n.Store().GetOutgoing(id)
	if err != nil || !unfinished(t.Status) {
		return i18n.New("这个任务已经结束")
	}
	return uiError(n.Store().CancelOutgoing(id))
}

// OpenFile 用系统默认程序打开历史里的一个文件。
func (s *Service) OpenFile(historyID int64, index int) error {
	path, err := s.filePath(historyID, index)
	if err != nil {
		return err
	}
	if s.cfg.Open == nil {
		return i18n.New("这个平台不支持打开文件")
	}
	return s.cfg.Open(path)
}

// RevealFile 在文件管理器里显示历史里的一个文件。
func (s *Service) RevealFile(historyID int64, index int) error {
	path, err := s.filePath(historyID, index)
	if err != nil {
		return err
	}
	if s.cfg.Reveal == nil {
		return i18n.New("这个平台不支持打开文件夹")
	}
	return s.cfg.Reveal(path)
}

// HistoryFiles 返回一条历史里每个文件在本机上的路径，顺序同历史；已被移走或删除的为空串。
// iOS 用它打开、分享收到的文件和存相册（M4），桌面端用 OpenFile / RevealFile。
func (s *Service) HistoryFiles(historyID int64) ([]string, error) {
	n, err := s.node()
	if err != nil {
		return nil, err
	}
	e, err := n.HistoryEntry(historyID)
	if err != nil {
		return nil, i18n.New("这条记录已经不存在")
	}
	out := make([]string, len(e.Body.Files))
	for i, f := range e.Body.Files {
		if p := s.localPath(*e, f); fileExists(p) {
			out[i] = p
		}
	}
	return out, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (s *Service) filePath(historyID int64, index int) (string, error) {
	n, err := s.node()
	if err != nil {
		return "", err
	}
	e, err := n.HistoryEntry(historyID)
	if err != nil {
		return "", i18n.New("这条记录已经不存在")
	}
	if index < 0 || index >= len(e.Body.Files) {
		return "", i18n.New("这条记录里没有这个文件")
	}
	path := s.localPath(*e, e.Body.Files[index])
	if _, err := os.Stat(path); err != nil {
		return "", i18n.New("文件已被移走或删除")
	}
	return path, nil
}

// Inbox 返回等待确认的接收。
func (s *Service) Inbox() ([]InboxItem, error) {
	n, err := s.node()
	if err != nil {
		return nil, err
	}
	entries, err := n.Store().PendingInbox(time.Now())
	if err != nil {
		return nil, uiError(err)
	}
	items := make([]InboxItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, InboxItem{ID: e.ID, PeerName: e.PeerName, Summary: e.Summary, Total: e.Total, ExpiresAt: e.ExpiresAt.UnixMilli()})
	}
	return items, nil
}

// DecideInbox 接受或拒绝一条等待确认的接收。
func (s *Service) DecideInbox(id int64, accept bool) error {
	n, err := s.node()
	if err != nil {
		return err
	}
	err = n.Store().DecideInbox(id, accept)
	if errors.Is(err, store.ErrNotPending) {
		return i18n.New("这条接收已经处理过或已过期")
	}
	return uiError(err)
}
